package task

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	DefaultHopLimit        = 2
	WorkerSummaryMaxBytes  = 16 * 1024
	DefaultIdempotencyDays = 30
)

type State string

const (
	StateQueued  State = "queued"
	StateRunning State = "running"
	// StateAwaitingInput is a running task blocked on a question it asked its
	// owner. The runner process is still alive, so this is non-terminal: it must
	// be swept on daemon restart like running, and the task can still complete
	// on its own if the agent proceeds without waiting.
	StateAwaitingInput State = "awaiting_input"
	StateDone          State = "done"
	StateFailed        State = "failed"
	StateDenied        State = "denied"
	StateCancelled     State = "cancelled"
)

type ErrorCode string

const (
	ErrorPeerUnreachable       ErrorCode = "peer_unreachable"
	ErrorPeerUnauthorized      ErrorCode = "peer_unauthorized"
	ErrorRunnerUnavailable     ErrorCode = "runner_unavailable"
	ErrorProfileUnavailable    ErrorCode = "profile_unavailable"
	ErrorProfileDenied         ErrorCode = "profile_denied"
	ErrorProfileAckRequired    ErrorCode = "profile_acknowledgement_required"
	ErrorCWDDenied             ErrorCode = "cwd_denied"
	ErrorDirtyWorktree         ErrorCode = "dirty_worktree"
	ErrorVersionMismatch       ErrorCode = "version_mismatch"
	ErrorTaskConflict          ErrorCode = "task_conflict"
	ErrorTimeout               ErrorCode = "timeout"
	ErrorNeedsHuman            ErrorCode = "needs_human"
	ErrorWorkerFailed          ErrorCode = "worker_failed"
	ErrorWorkerDaemonRestarted ErrorCode = "worker_daemon_restarted"
	ErrorCancelled             ErrorCode = "cancelled"
	ErrorResultTooLarge        ErrorCode = "result_too_large"
	ErrorTranscriptUnavailable ErrorCode = "transcript_unavailable"
)

type Record struct {
	ID                string          `json:"task_id"`
	OriginNodeID      string          `json:"origin_node_id"`
	OriginSessionID   string          `json:"origin_session_id"`
	WorkerNodeID      string          `json:"worker_node_id,omitempty"`
	State             State           `json:"state"`
	CWD               string          `json:"cwd"`
	Runner            string          `json:"runner"`
	ProfileRequested  string          `json:"profile_requested"`
	ProfileEffective  string          `json:"profile_effective,omitempty"`
	PromptHash        string          `json:"prompt_hash"`
	PayloadHash       string          `json:"payload_hash"`
	AllowDirty        bool            `json:"allow_dirty"`
	DelegationChain   []string        `json:"delegation_chain,omitempty"`
	HopCount          int             `json:"hop_count"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	StartedAt         time.Time       `json:"started_at,omitempty"`
	CompletedAt       time.Time       `json:"completed_at,omitempty"`
	TerminalErrorCode ErrorCode       `json:"terminal_error_code,omitempty"`
	ResultRef         string          `json:"result_ref,omitempty"`
	Result            *ResultEnvelope `json:"result,omitempty"`
}

type ResultEnvelope struct {
	Status                  State                  `json:"status"`
	Branch                  string                 `json:"branch,omitempty"`
	BaseCommit              string                 `json:"base_commit,omitempty"`
	HeadCommit              string                 `json:"head_commit,omitempty"`
	Pushed                  bool                   `json:"pushed"`
	Diffstat                string                 `json:"diffstat,omitempty"`
	FilesChanged            []string               `json:"files_changed,omitempty"`
	Tests                   TestClaims             `json:"tests"`
	CommandOutput           CommandOutput          `json:"command_output,omitempty"`
	QueryResult             *QueryResult           `json:"query_result,omitempty"`
	WorkerSummaryUntrusted  string                 `json:"worker_summary_untrusted,omitempty"`
	Usage                   Usage                  `json:"usage"`
	VerificationRequired    bool                   `json:"verification_required"`
	RecommendedOriginChecks []string               `json:"recommended_origin_checks,omitempty"`
	RunnerExtra             map[string]interface{} `json:"runner_extra,omitempty"`
}

type CommandOutput struct {
	Command   string `json:"command"`
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	StdoutRef string `json:"stdout_ref,omitempty"`
	StderrRef string `json:"stderr_ref,omitempty"`
	DurationS int64  `json:"duration_s"`
	// Truncated reports that captured output hit the runner's output limit, so
	// Stdout/Stderr are incomplete and the log refs should be read instead.
	Truncated bool `json:"truncated,omitempty"`
}

type QueryResult struct {
	Kind string                 `json:"kind"`
	Data map[string]interface{} `json:"data"`
}

type TestClaims struct {
	ClaimedPassed bool     `json:"claimed_passed"`
	Commands      []string `json:"commands,omitempty"`
	LogsRef       string   `json:"logs_ref,omitempty"`
}

type Usage struct {
	Tokens    int64   `json:"tokens"`
	Cost      float64 `json:"cost"`
	DurationS int64   `json:"duration_s"`
}

func NewID() (string, error) {
	var b [20]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate task id: %w", err)
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
	return "tsk_" + strings.ToLower(encoded), nil
}

// idRE matches the task IDs this system mints: a "tsk_" prefix followed by
// lowercase base32 (RFC 4648 alphabet, no padding). Validating against it is a
// security control, not cosmetics: task IDs flow into filepath.Join for log,
// artifact, worktree, and token paths, so an unconstrained caller-supplied ID
// (e.g. one containing "../") would be a path-traversal / arbitrary-file-write
// primitive on the worker. Any externally accepted task or follow-up id MUST
// pass ValidID before it is used to build a path.
var idRE = regexp.MustCompile(`^tsk_[a-z2-7]{1,64}$`)

// ValidID reports whether id is a well-formed, system-minted task id safe to
// use in filesystem paths. Reject anything else at trust boundaries.
func ValidID(id string) bool {
	return idRE.MatchString(id)
}

func DeterministicID(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	sum := h.Sum(nil)
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:20])
	return "tsk_" + strings.ToLower(encoded)
}

func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func PromptHash(prompt string) string {
	return SHA256Hex([]byte(prompt))
}

func IdempotencyKey(originNodeID, originSessionID, taskID string) string {
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(originNodeID)) + "." +
		enc.EncodeToString([]byte(originSessionID)) + "." +
		enc.EncodeToString([]byte(taskID))
}

func (s State) Valid() bool {
	switch s {
	case StateQueued, StateRunning, StateAwaitingInput, StateDone, StateFailed, StateDenied, StateCancelled:
		return true
	default:
		return false
	}
}

func (s State) Terminal() bool {
	switch s {
	case StateDone, StateFailed, StateDenied, StateCancelled:
		return true
	default:
		return false
	}
}

func ValidTransition(from, to State) bool {
	if !from.Valid() || !to.Valid() {
		return false
	}
	if from == to {
		return true
	}
	switch from {
	case StateQueued:
		return to == StateRunning || to == StateDenied || to == StateFailed || to == StateCancelled
	case StateRunning:
		return to == StateDone || to == StateFailed || to == StateCancelled || to == StateAwaitingInput
	case StateAwaitingInput:
		// Back to running when answered, but also straight to a terminal state:
		// the runner process keeps executing while the task displays
		// awaiting_input, so an agent that stops waiting and finishes must not
		// have its result rejected as an invalid transition.
		return to == StateRunning || to == StateDone || to == StateFailed || to == StateCancelled
	default:
		return false
	}
}

func (r Record) ValidateNew() error {
	if strings.TrimSpace(r.ID) == "" {
		return errors.New("task id is required")
	}
	if !ValidID(r.ID) {
		return errors.New("task id is malformed")
	}
	if strings.TrimSpace(r.OriginNodeID) == "" {
		return errors.New("origin node id is required")
	}
	if strings.TrimSpace(r.OriginSessionID) == "" {
		return errors.New("origin session id is required")
	}
	if strings.TrimSpace(r.CWD) == "" {
		return errors.New("cwd is required")
	}
	if strings.TrimSpace(r.Runner) == "" {
		return errors.New("runner is required")
	}
	if strings.TrimSpace(r.ProfileRequested) == "" {
		return errors.New("requested profile is required")
	}
	if strings.TrimSpace(r.PromptHash) == "" {
		return errors.New("prompt hash is required")
	}
	if strings.TrimSpace(r.PayloadHash) == "" {
		return errors.New("payload hash is required")
	}
	if r.State == "" {
		return errors.New("state is required")
	}
	if !r.State.Valid() {
		return fmt.Errorf("invalid task state %q", r.State)
	}
	if r.State != StateQueued {
		return fmt.Errorf("new task state must be %q", StateQueued)
	}
	if r.HopCount < 0 {
		return errors.New("hop count cannot be negative")
	}
	return nil
}

func ValidateResult(result ResultEnvelope) error {
	if !result.Status.Valid() {
		return fmt.Errorf("invalid result status %q", result.Status)
	}
	if !result.Status.Terminal() {
		return fmt.Errorf("result status must be terminal, got %q", result.Status)
	}
	if len([]byte(result.WorkerSummaryUntrusted)) > WorkerSummaryMaxBytes {
		return fmt.Errorf("worker summary exceeds %d bytes", WorkerSummaryMaxBytes)
	}
	if result.Status == StateDone && result.CommandOutput.Command != "" && result.CommandOutput.ExitCode != 0 {
		return fmt.Errorf("done result has non-zero command exit code %d", result.CommandOutput.ExitCode)
	}
	return nil
}
