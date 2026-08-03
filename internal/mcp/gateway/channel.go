package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cmesh/internal/audit"
	"cmesh/internal/mesh/rpc"
	"cmesh/internal/mesh/schema"
	"cmesh/internal/store"
	"cmesh/internal/task"
)

// The task channel gives a running task a bounded, audited conversation with
// its owner. It has two ends with two different credentials, and they must not
// be confused:
//
//   - task_post / task_ask run INSIDE a worker task. They authenticate with the
//     per-task token cmesh injected into that runner, and operate on the local
//     task that token names — resolved through the token's context file, never
//     from a caller-supplied id.
//
//   - task_send / task_messages run on the ORIGIN. They authenticate as the
//     task's owner and operate on a mirrored peer task.
//
// A task token must never reach the origin end. If a worker could call
// task_send it would answer its own question, manufacturing apparent owner
// approval — which would defeat the point of asking. That refusal is enforced
// in requireOwnerCaller and asserted in tests.

// defaultAskWaitSeconds is how long task_ask blocks for an answer before
// returning unanswered. It is deliberately shorter than a typical harness MCP
// tool timeout: returning "not answered yet, call again" keeps the agent in
// control, where a hung tool call would strand it.
const defaultAskWaitSeconds = 60

// maxAskWaitSeconds caps a single blocking wait.
const maxAskWaitSeconds = 600

// workerTaskFromToken resolves the task a per-task token was minted for.
//
// The context file names exactly one task, so that is the authorization key.
// requestedTaskID is accepted only to be checked: a body-supplied task id must
// never widen what a token reaches, the same rule that keeps a worker from
// choosing its own origin session on delegate.
func (g *Gateway) workerTaskFromToken(ctx context.Context, requestedTaskID string) (task.Record, error) {
	prov, ok := ctx.Value(provenanceContextKey{}).(gatewayProvenance)
	if !ok {
		return task.Record{}, errors.New("this tool runs inside a cmesh worker task and requires a task token; pass task_token from CMESH_TASK_TOKEN")
	}
	taskID := strings.TrimSpace(prov.TaskID)
	if taskID == "" {
		return task.Record{}, errors.New("task token context does not name a task")
	}
	if requested := strings.TrimSpace(requestedTaskID); requested != "" && requested != taskID {
		return task.Record{}, fmt.Errorf("task token is not authorized for task %q", requested)
	}
	rec, err := g.Store.GetTask(ctx, taskID)
	if err != nil {
		return task.Record{}, fmt.Errorf("task %s is not on this node: %w", taskID, err)
	}
	return rec, nil
}

// requireOwnerCaller rejects a per-task token on the origin end of the channel.
// Only the caller holding the gateway host token — this machine's own agent —
// may answer or steer a task.
func requireOwnerCaller(ctx context.Context) error {
	if _, scoped := taskTokenSession(ctx); scoped {
		return errors.New("a worker task token cannot answer or steer a task; only the task's owner may reply")
	}
	return nil
}

// postCall records a progress note from inside a worker task.
func (g *Gateway) postCall(ctx context.Context, requestedTaskID, body, clientMessageID string) (ToolResponse, error) {
	rec, err := g.workerTaskFromToken(ctx, requestedTaskID)
	if err != nil {
		return toolFailure(task.ErrorPeerUnauthorized, err.Error())
	}
	if strings.TrimSpace(body) == "" {
		return toolFailure(task.ErrorWorkerFailed, "body is required")
	}
	message, err := g.appendChannelMessage(ctx, rec, store.AppendMessageInput{
		TaskID:          rec.ID,
		Direction:       store.MessageDirectionFromWorker,
		Kind:            store.MessageKindNote,
		Body:            body,
		ClientMessageID: clientMessageID,
	})
	if err != nil {
		return messageFailure(err)
	}
	return ToolResponse{OK: true, Result: map[string]any{
		"task_id": rec.ID,
		"seq":     message.Seq,
		"posted":  true,
	}}, nil
}

// askCall records a question from inside a worker task and waits for an answer.
//
// The task shows awaiting_input while the question is open, so an operator can
// see why it is idle. Waiting is a poll loop over short queries rather than a
// held transaction: the store serializes writes on one connection, so blocking
// inside a transaction would stall every other task on this node.
//
// If no answer arrives before the deadline the call returns unanswered rather
// than hanging — the agent decides whether to proceed or ask again. Calling
// again with the same client_message_id resumes the same question instead of
// opening a second one.
func (g *Gateway) askCall(ctx context.Context, requestedTaskID, body, clientMessageID string, approval map[string]any, waitSeconds int) (ToolResponse, error) {
	rec, err := g.workerTaskFromToken(ctx, requestedTaskID)
	if err != nil {
		return toolFailure(task.ErrorPeerUnauthorized, err.Error())
	}
	if strings.TrimSpace(body) == "" {
		return toolFailure(task.ErrorWorkerFailed, "body is required")
	}
	kind := store.MessageKindQuestion
	if len(approval) > 0 {
		kind = store.MessageKindApprovalRequest
	}
	question, err := g.appendChannelMessage(ctx, rec, store.AppendMessageInput{
		TaskID:          rec.ID,
		Direction:       store.MessageDirectionFromWorker,
		Kind:            kind,
		Body:            body,
		Metadata:        approval,
		ClientMessageID: clientMessageID,
	})
	if err != nil {
		return messageFailure(err)
	}

	// Mark the task as blocked only while a fresh question is open. A resumed
	// call re-reads the existing question, so it must not re-transition a task
	// that has since moved on.
	if question.AnsweredSeq == 0 {
		g.setAwaitingInput(ctx, rec, true)
	}

	answer, answered := g.waitForAnswer(ctx, rec.ID, question.Seq, waitSeconds)
	// Leave awaiting_input on every return path, answered or not. The state is
	// operator-visible, and a worker that asks, times out, and proceeds on its
	// own judgment must not leave the task displaying "blocked on a question"
	// for the rest of its life.
	g.setAwaitingInput(ctx, rec, false)
	if !answered {
		return ToolResponse{OK: true, Result: map[string]any{
			"task_id":  rec.ID,
			"seq":      question.Seq,
			"answered": false,
			"guidance": "No answer yet. Continue if you can proceed safely, or call task_ask again with the same client_message_id to keep waiting for this question.",
		}}, nil
	}
	return ToolResponse{OK: true, Result: map[string]any{
		"task_id":  rec.ID,
		"seq":      question.Seq,
		"answered": true,
		// Named for what it is: the owner's reply is trusted direction, unlike
		// the worker-authored body on the inbound side.
		"answer":      answer.Body,
		"answered_by": "task_owner",
	}}, nil
}

// waitForAnswer polls for a reply to one question. It returns as soon as the
// question is answered, the task leaves awaiting_input (cancelled, failed, or
// completed elsewhere), or the deadline passes.
func (g *Gateway) waitForAnswer(ctx context.Context, taskID string, questionSeq int64, waitSeconds int) (store.TaskMessage, bool) {
	if waitSeconds <= 0 {
		waitSeconds = defaultAskWaitSeconds
	}
	if waitSeconds > maxAskWaitSeconds {
		waitSeconds = maxAskWaitSeconds
	}
	deadline := time.NewTimer(time.Duration(waitSeconds) * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if question, err := g.Store.TaskMessageBySeq(ctx, taskID, questionSeq); err == nil && question.AnsweredSeq > 0 {
			if answer, err := g.Store.TaskMessageBySeq(ctx, taskID, question.AnsweredSeq); err == nil {
				return answer, true
			}
		}
		select {
		case <-deadline.C:
			return store.TaskMessage{}, false
		case <-ctx.Done():
			return store.TaskMessage{}, false
		case <-ticker.C:
			// A task that reached a terminal state will never be answered.
			if rec, err := g.Store.GetTask(ctx, taskID); err == nil && rec.State.Terminal() {
				return store.TaskMessage{}, false
			}
		}
	}
}

// setAwaitingInput moves a running task in and out of awaiting_input. It is
// best-effort by design: the channel must never fail a task, and an invalid
// transition here means the task already moved on.
func (g *Gateway) setAwaitingInput(ctx context.Context, rec task.Record, blocked bool) {
	next := task.StateRunning
	if blocked {
		next = task.StateAwaitingInput
	}
	current, err := g.Store.GetTask(ctx, rec.ID)
	if err != nil || current.State.Terminal() || current.State == next {
		return
	}
	if !task.ValidTransition(current.State, next) {
		return
	}
	_, _ = g.Store.UpdateTaskState(ctx, rec.ID, store.StateUpdate{State: next})
}

// appendChannelMessage records a message and mirrors it to the audit log, so
// the conversation is auditable evidence like every other task event.
func (g *Gateway) appendChannelMessage(ctx context.Context, rec task.Record, in store.AppendMessageInput) (store.TaskMessage, error) {
	message, err := g.Store.AppendTaskMessage(ctx, in)
	if err != nil {
		return store.TaskMessage{}, err
	}
	if g.Audit != nil {
		event, auditErr := g.Audit.Append(audit.Event{
			Type:          "task.message." + message.Kind,
			TaskID:        rec.ID,
			OriginSession: rec.OriginSessionID,
			Runner:        rec.Runner,
			Profile:       rec.ProfileEffective,
			Source:        message.Direction,
			// The body is deliberately not audited: it is worker-authored text
			// already stored in task_messages, and copying it into the audit log
			// would duplicate untrusted content into the evidence record.
			Extra: map[string]any{"message_seq": message.Seq, "message_kind": message.Kind},
		})
		if auditErr == nil && g.Store != nil {
			_ = g.Store.RecordAuditEvent(ctx, event)
		}
	}
	return message, nil
}

// messagesCall reads a delegated task's channel from the origin.
func (g *Gateway) messagesCall(ctx context.Context, taskID string, after int64, limit int, wait bool, timeoutSeconds int) (ToolResponse, error) {
	return g.fetchOwnedTask(ctx, taskID, task.ErrorPeerUnreachable,
		func(client *rpc.Client, remoteTaskID string) (any, error) {
			return client.TaskMessages(ctx, remoteTaskID, after, limit, wait, timeoutSeconds)
		})
}

// sendCall answers a worker's question or steers a running task.
//
// A per-task token is refused here. A worker that could call this would answer
// its own question and manufacture apparent owner approval, which is precisely
// what asking is meant to prevent.
func (g *Gateway) sendCall(ctx context.Context, taskID, body, kind string, answeredSeq int64, clientMessageID string) (ToolResponse, error) {
	if err := requireOwnerCaller(ctx); err != nil {
		return toolFailure(task.ErrorPeerUnauthorized, err.Error())
	}
	if strings.TrimSpace(body) == "" {
		return toolFailure(task.ErrorWorkerFailed, "body is required")
	}
	return g.fetchOwnedTask(ctx, taskID, task.ErrorPeerUnreachable,
		func(client *rpc.Client, remoteTaskID string) (any, error) {
			return client.PostTaskMessage(ctx, remoteTaskID, schema.PostTaskMessageRequest{
				Kind:            kind,
				Body:            body,
				AnsweredSeq:     answeredSeq,
				ClientMessageID: clientMessageID,
			})
		})
}

// ApprovalReview is the origin-side verdict on a worker's approval request.
type ApprovalReview struct {
	// AutoDenied reports that the request asked for a capability the task's
	// profile never granted, so it can be refused without asking the human.
	AutoDenied bool   `json:"auto_denied"`
	Reason     string `json:"reason,omitempty"`
	// NeedsHuman reports that the request cannot be decided mechanically. Free
	// text touching authority is never auto-answered.
	NeedsHuman bool `json:"needs_human"`
}

// reviewApprovalRequest decides mechanically what it can about an approval
// request, and says so plainly when it cannot.
//
// A typed request names the capability it wants, so it can be checked against
// what the worker's profile actually granted: asking for something outside that
// set is refusable without troubling the human, because the worker would be
// denied by policy anyway. Anything else — free prose, or a request on a task
// whose granted profile we do not know — goes to the human. The fallback is
// deliberately "ask", never "allow".
func reviewApprovalRequest(metadata map[string]any, grantedProfile string, grantedCapabilities []string) ApprovalReview {
	capability := ""
	if metadata != nil {
		if value, ok := metadata["capability"].(string); ok {
			capability = strings.TrimSpace(value)
		}
	}
	if capability == "" {
		return ApprovalReview{NeedsHuman: true, Reason: "request does not name a capability, so it cannot be checked against the task's profile"}
	}
	if strings.TrimSpace(grantedProfile) == "" || len(grantedCapabilities) == 0 {
		return ApprovalReview{NeedsHuman: true, Reason: "the profile granted to this task is not recorded, so the request cannot be checked mechanically"}
	}
	for _, granted := range grantedCapabilities {
		if strings.EqualFold(strings.TrimSpace(granted), capability) {
			return ApprovalReview{NeedsHuman: true, Reason: fmt.Sprintf("capability %q is within profile %q; a human must still decide whether to allow this specific action", capability, grantedProfile)}
		}
	}
	return ApprovalReview{
		AutoDenied: true,
		Reason:     fmt.Sprintf("capability %q is outside profile %q, which this task was granted; the worker would be denied by policy anyway", capability, grantedProfile),
	}
}

// messageFailure maps store-level message errors onto stable tool error codes.
func messageFailure(err error) (ToolResponse, error) {
	switch {
	case errors.Is(err, store.ErrMessageLimitReached):
		return toolFailure(task.ErrorWorkerFailed, "task message limit reached; this task's channel is full")
	case errors.Is(err, store.ErrMessageTooLarge):
		return toolFailure(task.ErrorResultTooLarge, fmt.Sprintf("message body exceeds %d bytes", store.MaxMessageBodyBytes))
	default:
		return toolFailure(task.ErrorWorkerFailed, err.Error())
	}
}
