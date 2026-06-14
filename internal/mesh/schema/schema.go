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
	Provenance         Provenance    `json:"provenance"`
}

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

type CancelTaskRequest struct {
	Reason string `json:"reason,omitempty"`
}

type CancelTaskResponse struct {
	TaskID string     `json:"task_id"`
	State  task.State `json:"state"`
	Error  *Error     `json:"error,omitempty"`
}
