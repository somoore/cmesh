package runner

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type ErrorCode string

const (
	ErrPeerUnreachable       ErrorCode = "peer_unreachable"
	ErrPeerUnauthorized      ErrorCode = "peer_unauthorized"
	ErrRunnerUnavailable     ErrorCode = "runner_unavailable"
	ErrProfileUnavailable    ErrorCode = "profile_unavailable"
	ErrProfileDenied         ErrorCode = "profile_denied"
	ErrCWDDenied             ErrorCode = "cwd_denied"
	ErrDirtyWorktree         ErrorCode = "dirty_worktree"
	ErrVersionMismatch       ErrorCode = "version_mismatch"
	ErrTaskConflict          ErrorCode = "task_conflict"
	ErrTimeout               ErrorCode = "timeout"
	ErrNeedsHuman            ErrorCode = "needs_human"
	ErrWorkerFailed          ErrorCode = "worker_failed"
	ErrDaemonRestarted       ErrorCode = "worker_daemon_restarted"
	ErrCancelled             ErrorCode = "cancelled"
	ErrResultTooLarge        ErrorCode = "result_too_large"
	ErrTranscriptUnavailable ErrorCode = "transcript_unavailable"
)

type Error struct {
	Code ErrorCode
	Op   string
	Err  error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Op == "" {
		return fmt.Sprintf("%s: %v", e.Code, e.Err)
	}
	return fmt.Sprintf("%s: %s: %v", e.Code, e.Op, e.Err)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func E(code ErrorCode, op string, err error) error {
	if err == nil {
		err = errors.New(string(code))
	}
	return &Error{Code: code, Op: op, Err: err}
}

func CodeOf(err error) ErrorCode {
	var re *Error
	if errors.As(err, &re) {
		return re.Code
	}
	return ""
}

type Runner interface {
	Name() string
	Manifest() CapabilityManifest
	Check(profile string) error
	Run(req RunRequest) (Result, error)
}

type CapabilityManifest struct {
	SchemaVersion string                     `json:"schema_version"`
	Runner        string                     `json:"runner"`
	Version       string                     `json:"version,omitempty"`
	Profiles      map[string]ProfileManifest `json:"profiles"`
}

type ProfileManifest struct {
	Claims                []string          `json:"claims"`
	NativeFlags           []string          `json:"native_flags,omitempty"`
	NativeControls        map[string]string `json:"native_controls,omitempty"`
	CredentialSource      string            `json:"credential_source,omitempty"`
	Billing               string            `json:"billing,omitempty"`
	EnforcementConfidence string            `json:"enforcement_confidence"`
	KnownGaps             []string          `json:"known_gaps,omitempty"`
	Bare                  bool              `json:"bare,omitempty"`
}

type RunRequest struct {
	Context     context.Context
	TaskID      string
	Prompt      string
	CWD         string
	Profile     string
	AllowDirty  bool
	Timeout     time.Duration
	Env         map[string]string
	LogDir      string
	Metadata    TaskMetadata
	OutputLimit int64
}

type TaskMetadata struct {
	OriginNodeID    string
	OriginSessionID string
	WorkerNodeID    string
	Runner          string
	Profile         string
	TaskID          string
	DelegationChain []string
	HopCount        int
	ResumeSessionID string
}

type Result struct {
	Status                  string         `json:"status"`
	Branch                  string         `json:"branch,omitempty"`
	BaseCommit              string         `json:"base_commit,omitempty"`
	HeadCommit              string         `json:"head_commit,omitempty"`
	Pushed                  bool           `json:"pushed"`
	Diffstat                string         `json:"diffstat,omitempty"`
	FilesChanged            []string       `json:"files_changed,omitempty"`
	Tests                   TestClaims     `json:"tests"`
	WorkerSummaryUntrusted  string         `json:"worker_summary_untrusted"`
	Usage                   Usage          `json:"usage"`
	VerificationRequired    bool           `json:"verification_required"`
	RecommendedOriginChecks []string       `json:"recommended_origin_checks,omitempty"`
	RunnerExtra             map[string]any `json:"runner_extra,omitempty"`
	TranscriptRef           string         `json:"-"`
}

type TestClaims struct {
	ClaimedPassed bool     `json:"claimed_passed"`
	Commands      []string `json:"commands,omitempty"`
	LogsRef       string   `json:"logs_ref,omitempty"`
}

type Usage struct {
	Tokens    int     `json:"tokens"`
	Cost      float64 `json:"cost"`
	DurationS int64   `json:"duration_s"`
}
