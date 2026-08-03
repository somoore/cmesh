package schema

import (
	"time"

	"cmesh/internal/task"
)

const (
	Version             = "v1"
	SchemaVersionValue  = "v1"
	HeaderSchemaVersion = "Cmesh-Schema-Version"
)

type Error struct {
	Code   task.ErrorCode `json:"code"`
	Detail string         `json:"detail"`
}

type HelloResponse struct {
	NodeID         string    `json:"node_id"`
	NodeName       string    `json:"node_name"`
	StableID       string    `json:"stable_id,omitempty"`
	CmeshVersion   string    `json:"cmesh_version,omitempty"`
	CmeshCommit    string    `json:"cmesh_commit,omitempty"`
	CmeshDate      string    `json:"cmesh_date,omitempty"`
	SchemaVersions []string  `json:"schema_versions"`
	Runners        []Runner  `json:"runners"`
	Capabilities   []string  `json:"capabilities"`
	ServerTime     time.Time `json:"server_time"`
}

type Runner struct {
	Name     string   `json:"name"`
	Version  string   `json:"version,omitempty"`
	Profiles []string `json:"profiles,omitempty"`
}

type PeerIdentity struct {
	NodeID   string   `json:"node_id"`
	NodeName string   `json:"node_name,omitempty"`
	User     string   `json:"user,omitempty"`
	Tags     []string `json:"tags,omitempty"`
}

type Provenance struct {
	OriginNodeID    string   `json:"origin_node_id"`
	OriginSessionID string   `json:"origin_session_id"`
	OriginHarness   string   `json:"origin_harness,omitempty"`
	OriginVersion   string   `json:"origin_version,omitempty"`
	WorkerNodeID    string   `json:"worker_node_id,omitempty"`
	WorkerHarness   string   `json:"worker_harness,omitempty"`
	WorkerVersion   string   `json:"worker_version,omitempty"`
	PromptHash      string   `json:"prompt_hash"`
	DelegationChain []string `json:"delegation_chain,omitempty"`
	HopCount        int      `json:"hop_count"`
}

type CreateTaskRequest struct {
	TaskID             string        `json:"task_id"`
	OriginNodeID       string        `json:"origin_node_id"`
	OriginSessionID    string        `json:"origin_session_id"`
	TaskKind           string        `json:"task_kind,omitempty"`
	Query              *QueryRequest `json:"query,omitempty"`
	Prompt             string        `json:"prompt"`
	CWD                string        `json:"cwd"`
	Runner             string        `json:"runner,omitempty"`
	Profile            string        `json:"profile,omitempty"`
	ProfileSource      string        `json:"profile_source,omitempty"`
	ProfileContextID   string        `json:"profile_context_id,omitempty"`
	TimeoutSeconds     int           `json:"timeout_s,omitempty"`
	Wait               bool          `json:"wait,omitempty"`
	AllowDirty         bool          `json:"allow_dirty,omitempty"`
	FollowupTaskID     string        `json:"followup_task_id,omitempty"`
	FollowupBaseCommit string        `json:"followup_base_commit,omitempty"`
	ResumeSessionID    string        `json:"resume_session_id,omitempty"`
	// Handoff carries a source peer's branch so this task can continue it. It
	// travels in the create request rather than a separate upload so it passes
	// through every gate a normal dispatch does, and so the seed is present
	// before the task's runner reaches git preparation.
	Handoff    *HandoffSeed `json:"handoff,omitempty"`
	Provenance Provenance   `json:"provenance"`
}

// HandoffSeed is a source task's branch, packed as a git bundle, plus the refs
// needed to apply it. BundleBase64 is bounded by MaxHandoffBundleBytes.
//
// It carries structured refs only. A worker's prose never seeds another
// worker's prompt: the receiving agent reads the diff, not the previous
// agent's narration.
type HandoffSeed struct {
	SourceTaskID string   `json:"source_task_id"`
	SourcePeer   string   `json:"source_peer,omitempty"`
	Branch       string   `json:"branch"`
	BaseCommit   string   `json:"base_commit"`
	HeadCommit   string   `json:"head_commit,omitempty"`
	FilesChanged []string `json:"files_changed,omitempty"`
	BundleBase64 string   `json:"bundle_base64"`
}

// MaxHandoffBundleBytes caps a seed bundle. A handoff carries one task's branch,
// not a repository; anything larger belongs on a shared remote.
const MaxHandoffBundleBytes = 32 << 20

type QueryRequest struct {
	Kind   string         `json:"kind"`
	Params map[string]any `json:"params,omitempty"`
	CWD    string         `json:"cwd,omitempty"`
}

type CreateTaskResponse struct {
	TaskID           string     `json:"task_id"`
	State            task.State `json:"state"`
	Replayed         bool       `json:"replayed"`
	RequestedProfile string     `json:"requested_profile,omitempty"`
	EffectiveProfile string     `json:"effective_profile,omitempty"`
	ProfileSource    string     `json:"profile_source,omitempty"`
	ProfileContextID string     `json:"profile_context_id,omitempty"`
	Error            *Error     `json:"error,omitempty"`
}

// TaskEvent is one recorded step in a task's life, as served to the origin.
//
// It is a deliberate projection of the worker's local audit record rather than
// a pass-through of it. The stored audit event also carries error_detail and a
// free-form extra map, which can hold worker-side filesystem paths and other
// local detail; those stay in the worker's own audit log, which is the artifact
// for investigating that machine. What crosses the mesh is the progress the
// origin needs to narrate a running task: what happened, when, under which
// profile and runner, and — for a denial or failure — the stable error code.
//
// Adding a field here widens what a delegating peer learns about the worker, so
// prefer a named field over reintroducing a passthrough map.
type TaskEvent struct {
	Seq            int64     `json:"seq"`
	EventID        string    `json:"event_id"`
	EventType      string    `json:"event_type"`
	TaskID         string    `json:"task_id,omitempty"`
	NodeID         string    `json:"node_id,omitempty"`
	Timestamp      time.Time `json:"timestamp"`
	Runner         string    `json:"runner,omitempty"`
	Profile        string    `json:"profile,omitempty"`
	PolicyDecision string    `json:"policy_decision,omitempty"`
	ErrorCode      string    `json:"error_code,omitempty"`
}

// TaskEventsResponse is an incremental page of task events. NextSeq is the
// cursor to pass back as `after`; HasMore reports that the page was truncated
// by the limit and another read will return more immediately.
type TaskEventsResponse struct {
	TaskID  string      `json:"task_id"`
	Events  []TaskEvent `json:"events"`
	NextSeq int64       `json:"next_seq"`
	HasMore bool        `json:"has_more"`
	Error   *Error      `json:"error,omitempty"`
}

// TaskMessage is one entry in a task's channel.
//
// BodyUntrusted is named for what it is on the inbound direction: text an agent
// on the worker wrote. Treat it as a claim, exactly like
// worker_summary_untrusted — never as an instruction to act on. Messages from
// the origin carry the owner's own words and are direction, not evidence.
type TaskMessage struct {
	MessageID     string         `json:"message_id"`
	Seq           int64          `json:"seq"`
	Direction     string         `json:"direction"`
	Kind          string         `json:"kind"`
	BodyUntrusted string         `json:"body_untrusted,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	AnsweredSeq   int64          `json:"answered_seq,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
}

// TaskMessagesResponse is an incremental page of a task's channel.
type TaskMessagesResponse struct {
	TaskID   string        `json:"task_id"`
	Messages []TaskMessage `json:"messages"`
	NextSeq  int64         `json:"next_seq"`
	HasMore  bool          `json:"has_more"`
	Error    *Error        `json:"error,omitempty"`
}

// PostTaskMessageRequest is the origin answering a question or steering a task.
type PostTaskMessageRequest struct {
	Kind            string `json:"kind,omitempty"`
	Body            string `json:"body"`
	AnsweredSeq     int64  `json:"answered_seq,omitempty"`
	ClientMessageID string `json:"client_message_id,omitempty"`
}

// PostTaskMessageResponse confirms a recorded message.
type PostTaskMessageResponse struct {
	TaskID  string `json:"task_id"`
	Seq     int64  `json:"seq"`
	Kind    string `json:"kind"`
	Error   *Error `json:"error,omitempty"`
	Message string `json:"message,omitempty"`
}

type TaskStatusResponse struct {
	TaskID            string         `json:"task_id"`
	State             task.State     `json:"state"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
	ElapsedSeconds    int64          `json:"elapsed_s"`
	LastActivity      string         `json:"last_activity,omitempty"`
	TerminalErrorCode task.ErrorCode `json:"terminal_error_code,omitempty"`
	Error             *Error         `json:"error,omitempty"`
}

type TaskResultResponse struct {
	TaskID string               `json:"task_id"`
	Result *task.ResultEnvelope `json:"result,omitempty"`
	Error  *Error               `json:"error,omitempty"`
}

type TaskListResponse struct {
	Tasks []task.Record `json:"tasks"`
}

type TaskTranscriptResponse struct {
	TaskID string `json:"task_id"`
	Stdout string `json:"stdout,omitempty"`
	Stderr string `json:"stderr,omitempty"`
}

type PeerSmokeRequest struct {
	CWD            string `json:"cwd"`
	Profile        string `json:"profile,omitempty"`
	TimeoutSeconds int    `json:"timeout_s,omitempty"`
}

type PeerSmokeResponse struct {
	Peer           string               `json:"peer"`
	NodeID         string               `json:"node_id,omitempty"`
	NodeName       string               `json:"node_name,omitempty"`
	TaskID         string               `json:"task_id,omitempty"`
	State          task.State           `json:"state,omitempty"`
	ElapsedSeconds int64                `json:"elapsed_s,omitempty"`
	Checks         map[string]bool      `json:"checks,omitempty"`
	Result         *task.ResultEnvelope `json:"result,omitempty"`
	Error          *Error               `json:"error,omitempty"`
}

type ProfileLeaseRequest struct {
	Profile         string   `json:"profile"`
	CWD             string   `json:"cwd,omitempty"`
	CWDScope        string   `json:"cwd_scope,omitempty"`
	TTLSeconds      int64    `json:"ttl_s"`
	Capabilities    []string `json:"capabilities,omitempty"`
	OriginSessionID string   `json:"origin_session_id,omitempty"`
}

type ProfileLease struct {
	LeaseID        string    `json:"lease_id"`
	OriginNodeID   string    `json:"origin_node_id"`
	OriginStableID string    `json:"origin_stable_id,omitempty"`
	OriginName     string    `json:"origin_name,omitempty"`
	Profile        string    `json:"profile"`
	CWD            string    `json:"cwd,omitempty"`
	CWDScope       string    `json:"cwd_scope,omitempty"`
	Capabilities   []string  `json:"capabilities,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	ExpiresAt      time.Time `json:"expires_at"`
	RevokedAt      time.Time `json:"revoked_at,omitempty"`
}

type ProfileLeaseListResponse struct {
	Leases []ProfileLease `json:"leases"`
}

type CancelTaskRequest struct {
	Reason string `json:"reason,omitempty"`
}

type CancelTaskResponse struct {
	TaskID string     `json:"task_id"`
	State  task.State `json:"state"`
	Error  *Error     `json:"error,omitempty"`
}
