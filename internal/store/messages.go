package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Message directions. Inbound is worker-authored and untrusted; outbound is
// authored by the task's owner.
const (
	MessageDirectionFromWorker = "from_worker"
	MessageDirectionFromOrigin = "from_origin"
)

// Message kinds.
const (
	MessageKindNote            = "note"             // worker progress, no reply expected
	MessageKindQuestion        = "question"         // worker asks, task blocks
	MessageKindApprovalRequest = "approval_request" // worker asks for a specific capability
	MessageKindAnswer          = "answer"           // origin replies to a question
	MessageKindSteer           = "steer"            // origin adds direction, unprompted
)

// MaxMessageBodyBytes bounds one message body. It matches the result envelope's
// summary cap: both are worker-authored text crossing the same boundary.
const MaxMessageBodyBytes = 16 * 1024

// MaxMessagesPerTask bounds a single task's conversation. A runaway worker must
// not be able to grow the store without limit; hitting the cap is a visible
// error, not a silent drop.
const MaxMessagesPerTask = 200

// ErrMessageLimitReached is returned when a task has hit MaxMessagesPerTask.
var ErrMessageLimitReached = errors.New("task message limit reached")

// ErrMessageTooLarge is returned when a body exceeds MaxMessageBodyBytes.
var ErrMessageTooLarge = errors.New("task message body too large")

// TaskMessage is one entry in a task's channel.
type TaskMessage struct {
	MessageID string         `json:"message_id"`
	TaskID    string         `json:"task_id"`
	Seq       int64          `json:"seq"`
	Direction string         `json:"direction"`
	Kind      string         `json:"kind"`
	Body      string         `json:"body_untrusted,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	// AnsweredSeq links an answer to the question it replies to, and is set on
	// the question once answered so a resumed task_ask can find its reply.
	AnsweredSeq     int64     `json:"answered_seq,omitempty"`
	ClientMessageID string    `json:"client_message_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// AppendMessageInput describes a message to record.
type AppendMessageInput struct {
	TaskID          string
	Direction       string
	Kind            string
	Body            string
	Metadata        map[string]any
	AnsweredSeq     int64
	ClientMessageID string
}

// AppendTaskMessage records one message and returns it with its allocated seq.
//
// A repeated ClientMessageID on the same task returns the existing message
// rather than appending a duplicate, so a runner that retries a tool call after
// a timeout does not post twice.
func (s *Store) AppendTaskMessage(ctx context.Context, in AppendMessageInput) (TaskMessage, error) {
	if strings.TrimSpace(in.TaskID) == "" {
		return TaskMessage{}, errors.New("task id is required")
	}
	switch in.Direction {
	case MessageDirectionFromWorker, MessageDirectionFromOrigin:
	default:
		return TaskMessage{}, fmt.Errorf("invalid message direction %q", in.Direction)
	}
	switch in.Kind {
	case MessageKindNote, MessageKindQuestion, MessageKindApprovalRequest, MessageKindAnswer, MessageKindSteer:
	default:
		return TaskMessage{}, fmt.Errorf("invalid message kind %q", in.Kind)
	}
	if len(in.Body) > MaxMessageBodyBytes {
		return TaskMessage{}, ErrMessageTooLarge
	}

	if existing, found, err := s.taskMessageByClientID(ctx, in.TaskID, in.ClientMessageID); err != nil {
		return TaskMessage{}, err
	} else if found {
		return existing, nil
	}

	metadata := ""
	if len(in.Metadata) > 0 {
		encoded, err := json.Marshal(in.Metadata)
		if err != nil {
			return TaskMessage{}, fmt.Errorf("encode message metadata: %w", err)
		}
		metadata = string(encoded)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskMessage{}, fmt.Errorf("begin append task message: %w", err)
	}
	defer rollback(tx)

	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_messages WHERE task_id = ?`, in.TaskID).Scan(&count); err != nil {
		return TaskMessage{}, fmt.Errorf("count task messages: %w", err)
	}
	if count >= MaxMessagesPerTask {
		return TaskMessage{}, ErrMessageLimitReached
	}

	// Allocate in the same transaction as the insert, matching task_events, so
	// concurrent writers cannot share a cursor value.
	var seq int64
	if err := tx.QueryRowContext(ctx, `UPDATE task_message_seq SET next_seq = next_seq + 1 WHERE id = 0 RETURNING next_seq - 1`).Scan(&seq); err != nil {
		return TaskMessage{}, fmt.Errorf("allocate task message seq: %w", err)
	}

	messageID, err := newMessageID()
	if err != nil {
		return TaskMessage{}, err
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO task_messages(message_id, task_id, seq, direction, kind, body_untrusted, metadata_json, answered_seq, client_message_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		messageID, in.TaskID, seq, in.Direction, in.Kind, in.Body, metadata, in.AnsweredSeq, in.ClientMessageID, formatTime(now),
	); err != nil {
		return TaskMessage{}, fmt.Errorf("insert task message: %w", err)
	}

	// An answer marks its question answered, so a resumed task_ask finds the
	// reply by looking at its own question rather than rescanning the channel.
	if in.Kind == MessageKindAnswer && in.AnsweredSeq > 0 {
		if _, err := tx.ExecContext(ctx,
			`UPDATE task_messages SET answered_seq = ? WHERE task_id = ? AND seq = ?`,
			seq, in.TaskID, in.AnsweredSeq); err != nil {
			return TaskMessage{}, fmt.Errorf("link answer to question: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return TaskMessage{}, fmt.Errorf("commit task message: %w", err)
	}
	return TaskMessage{
		MessageID:       messageID,
		TaskID:          in.TaskID,
		Seq:             seq,
		Direction:       in.Direction,
		Kind:            in.Kind,
		Body:            in.Body,
		Metadata:        in.Metadata,
		AnsweredSeq:     in.AnsweredSeq,
		ClientMessageID: in.ClientMessageID,
		CreatedAt:       now,
	}, nil
}

func (s *Store) taskMessageByClientID(ctx context.Context, taskID, clientMessageID string) (TaskMessage, bool, error) {
	if strings.TrimSpace(clientMessageID) == "" {
		return TaskMessage{}, false, nil
	}
	messages, err := s.queryTaskMessages(ctx,
		`SELECT message_id, task_id, seq, direction, kind, body_untrusted, metadata_json, answered_seq, client_message_id, created_at
		 FROM task_messages WHERE task_id = ? AND client_message_id = ? LIMIT 1`,
		taskID, clientMessageID)
	if err != nil {
		return TaskMessage{}, false, err
	}
	if len(messages) == 0 {
		return TaskMessage{}, false, nil
	}
	return messages[0], true, nil
}

// ListTaskMessages returns a task's messages with seq greater than after,
// oldest first.
func (s *Store) ListTaskMessages(ctx context.Context, taskID string, after int64, limit int) ([]TaskMessage, error) {
	if limit <= 0 {
		limit = DefaultTaskEventLimit
	}
	if limit > MaxTaskEventLimit {
		limit = MaxTaskEventLimit
	}
	return s.queryTaskMessages(ctx,
		`SELECT message_id, task_id, seq, direction, kind, body_untrusted, metadata_json, answered_seq, client_message_id, created_at
		 FROM task_messages WHERE task_id = ? AND seq > ? ORDER BY seq ASC LIMIT ?`,
		taskID, after, limit)
}

// TaskMessageBySeq returns one message by its seq.
func (s *Store) TaskMessageBySeq(ctx context.Context, taskID string, seq int64) (TaskMessage, error) {
	messages, err := s.queryTaskMessages(ctx,
		`SELECT message_id, task_id, seq, direction, kind, body_untrusted, metadata_json, answered_seq, client_message_id, created_at
		 FROM task_messages WHERE task_id = ? AND seq = ? LIMIT 1`,
		taskID, seq)
	if err != nil {
		return TaskMessage{}, err
	}
	if len(messages) == 0 {
		return TaskMessage{}, ErrNotFound
	}
	return messages[0], nil
}

// LatestTaskMessageSeq returns the highest seq recorded for a task, or 0.
func (s *Store) LatestTaskMessageSeq(ctx context.Context, taskID string) (int64, error) {
	var seq sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(seq) FROM task_messages WHERE task_id = ?`, taskID).Scan(&seq); err != nil {
		return 0, fmt.Errorf("query latest task message seq: %w", err)
	}
	if !seq.Valid {
		return 0, nil
	}
	return seq.Int64, nil
}

func (s *Store) queryTaskMessages(ctx context.Context, query string, args ...any) ([]TaskMessage, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query task messages: %w", err)
	}
	defer rows.Close()
	var out []TaskMessage
	for rows.Next() {
		var message TaskMessage
		var metadata, created string
		if err := rows.Scan(&message.MessageID, &message.TaskID, &message.Seq, &message.Direction, &message.Kind,
			&message.Body, &metadata, &message.AnsweredSeq, &message.ClientMessageID, &created); err != nil {
			return nil, fmt.Errorf("scan task message: %w", err)
		}
		if metadata != "" {
			decoded := map[string]any{}
			if err := json.Unmarshal([]byte(metadata), &decoded); err == nil {
				message.Metadata = decoded
			}
		}
		message.CreatedAt, err = parseTime(created)
		if err != nil {
			return nil, fmt.Errorf("decode task message created_at: %w", err)
		}
		out = append(out, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate task messages: %w", err)
	}
	return out, nil
}

func newMessageID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate message id: %w", err)
	}
	return "msg_" + hex.EncodeToString(buf), nil
}
