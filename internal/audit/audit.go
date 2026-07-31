package audit

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Event struct {
	EventID        string         `json:"event_id"`
	Timestamp      time.Time      `json:"timestamp"`
	NodeID         string         `json:"node_id"`
	Type           string         `json:"type"`
	TaskID         string         `json:"task_id,omitempty"`
	PeerIdentity   string         `json:"peer_identity,omitempty"`
	OriginSession  string         `json:"origin_session,omitempty"`
	Chain          []string       `json:"chain,omitempty"`
	Runner         string         `json:"runner,omitempty"`
	Profile        string         `json:"profile,omitempty"`
	PolicyDecision string         `json:"policy_decision,omitempty"`
	Source         string         `json:"source"`
	Claims         []string       `json:"claims,omitempty"`
	UsedControls   map[string]any `json:"used_controls,omitempty"`
	ErrorCode      string         `json:"error_code,omitempty"`
	ErrorDetail    string         `json:"error_detail,omitempty"`
	Extra          map[string]any `json:"extra,omitempty"`
}

type Writer struct {
	path   string
	nodeID string
	mu     sync.Mutex
}

func NewWriter(path, nodeID string) *Writer {
	return &Writer{path: path, nodeID: nodeID}
}

func (w *Writer) Append(event Event) (Event, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if event.EventID == "" {
		id, err := NewEventID()
		if err != nil {
			return Event{}, err
		}
		event.EventID = id
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	if event.NodeID == "" {
		event.NodeID = w.nodeID
	}
	// The audit log records peer identities, prompts hashes, profiles, and policy
	// decisions, so its directory is owner-only like the store's — the log file
	// itself is already 0600.
	if err := os.MkdirAll(filepath.Dir(w.path), 0o700); err != nil {
		return Event{}, err
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return Event{}, err
	}
	defer f.Close()
	line, err := json.Marshal(event)
	if err != nil {
		return Event{}, err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return Event{}, err
	}
	return event, nil
}

func NewEventID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "evt_" + hex.EncodeToString(b[:]), nil
}
