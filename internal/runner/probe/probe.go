package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"cmesh/internal/gitx"
	"cmesh/internal/profile"
	"cmesh/internal/runner"
)

const Name = profile.RunnerProbe

type Runner struct {
	profiles map[string]ProfileConfig
}

type ProfileConfig struct {
	Capabilities          []string
	MaxOutputBytes        int64
	EnforcementConfidence string
}

func New(profiles map[string]ProfileConfig) *Runner {
	if len(profiles) == 0 {
		profiles = map[string]ProfileConfig{
			profile.Query: {
				Capabilities:          []string{profile.CapDiskInfo, profile.CapSystemInfo, profile.CapRepoStatus},
				MaxOutputBytes:        65536,
				EnforcementConfidence: "high",
			},
		}
	}
	return &Runner{profiles: profiles}
}

func (r *Runner) Name() string {
	return Name
}

func (r *Runner) Manifest() runner.CapabilityManifest {
	profiles := make(map[string]runner.ProfileManifest, len(r.profiles))
	for name, prof := range r.profiles {
		confidence := prof.EnforcementConfidence
		if confidence == "" {
			confidence = "high"
		}
		profiles[name] = runner.ProfileManifest{
			Claims:                append([]string(nil), prof.Capabilities...),
			NativeControls:        map[string]string{"execution": "builtin-probes", "shell": "denied", "writes": "denied"},
			CredentialSource:      "none",
			Billing:               "none",
			EnforcementConfidence: confidence,
			KnownGaps:             []string{"repo_status uses git read-only commands when git is available"},
		}
	}
	return runner.CapabilityManifest{SchemaVersion: "runner_manifest.v1", Runner: Name, Profiles: profiles}
}

func (r *Runner) Check(profileName string) error {
	if _, ok := r.profiles[profileName]; !ok {
		return runner.E(runner.ErrProfileUnavailable, "select probe profile", fmt.Errorf("%q", profileName))
	}
	return nil
}

func (r *Runner) Run(req runner.RunRequest) (runner.Result, error) {
	prof, ok := r.profiles[req.Profile]
	if !ok {
		return runner.Result{}, runner.E(runner.ErrProfileUnavailable, "select probe profile", fmt.Errorf("%q", req.Profile))
	}
	kind := strings.TrimSpace(req.QueryKind)
	if kind == "" {
		return runner.Result{}, runner.E(runner.ErrWorkerFailed, "probe query", fmt.Errorf("query kind is required"))
	}
	if !contains(prof.Capabilities, kind) {
		return runner.Result{}, runner.E(runner.ErrProfileDenied, "probe query", fmt.Errorf("profile %q does not grant %s", req.Profile, kind))
	}
	started := time.Now()
	out, err := r.runProbe(req.Context, kind, req.QueryParams, req.CWD)
	result := runner.Result{
		Status: "done",
		Tests: runner.TestClaims{
			ClaimedPassed: false,
			Commands:      []string{"probe:" + kind},
		},
		CommandOutput: runner.CommandOutput{
			Command:   "probe:" + kind,
			ExitCode:  0,
			Stdout:    encode(out),
			DurationS: int64(time.Since(started) / time.Second),
		},
		WorkerSummaryUntrusted:  encode(out),
		VerificationRequired:    false,
		RecommendedOriginChecks: []string{"parse command_output.stdout as JSON and verify kind=" + kind},
		RunnerExtra: map[string]any{
			"runner":     Name,
			"query_kind": kind,
			"no_shell":   true,
			"no_writes":  true,
		},
	}
	if err != nil {
		result.CommandOutput.ExitCode = 1
		result.CommandOutput.Stderr = err.Error()
		return result, err
	}
	return result, nil
}

func (r *Runner) runProbe(ctx context.Context, kind string, params map[string]any, cwd string) (map[string]any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, runner.E(runner.ErrCancelled, "probe query", ctx.Err())
	default:
	}
	switch kind {
	case profile.CapSystemInfo:
		host, _ := os.Hostname()
		return map[string]any{
			"kind":         kind,
			"hostname":     host,
			"goos":         runtime.GOOS,
			"goarch":       runtime.GOARCH,
			"num_cpu":      runtime.NumCPU(),
			"cwd":          cwd,
			"generated_at": time.Now().UTC().Format(time.RFC3339),
		}, nil
	case profile.CapDiskInfo:
		path := stringParam(params, "path")
		if path == "" {
			path = stringParam(params, "volume")
		}
		if path == "" {
			path = cwd
		}
		if path == "" {
			path = "."
		}
		info, err := diskInfo(path)
		if err != nil {
			return nil, runner.E(runner.ErrWorkerFailed, "probe disk_info", err)
		}
		info["kind"] = kind
		return info, nil
	case profile.CapRepoStatus:
		if strings.TrimSpace(cwd) == "" {
			return nil, runner.E(runner.ErrCWDDenied, "probe repo_status", fmt.Errorf("cwd is required"))
		}
		repo, err := gitx.Open(cwd)
		if err != nil {
			return nil, runner.E(runner.ErrWorkerFailed, "probe repo_status", err)
		}
		status, err := repo.Status(ctx)
		if err != nil {
			return nil, runner.E(runner.ErrWorkerFailed, "probe repo_status", err)
		}
		return map[string]any{
			"kind":        kind,
			"is_repo":     status.IsRepo,
			"dirty":       status.Dirty,
			"porcelain":   status.Porcelain,
			"branch":      status.Branch,
			"head_commit": status.HeadCommit,
			"root":        status.Root,
		}, nil
	case profile.CapNetworkCheck:
		host := stringParam(params, "host")
		if host == "" {
			host = stringParam(params, "address")
		}
		if host == "" {
			return nil, runner.E(runner.ErrWorkerFailed, "probe network_check", fmt.Errorf("params.host is required"))
		}
		port := intParam(params, "port")
		if port <= 0 || port > 65535 {
			return nil, runner.E(runner.ErrWorkerFailed, "probe network_check", fmt.Errorf("params.port must be 1-65535"))
		}
		timeout := 5 * time.Second
		started := time.Now()
		conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err != nil {
			return map[string]any{
				"kind":        kind,
				"host":        host,
				"port":        port,
				"reachable":   false,
				"error":       err.Error(),
				"duration_ms": time.Since(started).Milliseconds(),
			}, nil
		}
		_ = conn.Close()
		return map[string]any{
			"kind":        kind,
			"host":        host,
			"port":        port,
			"reachable":   true,
			"duration_ms": time.Since(started).Milliseconds(),
		}, nil
	default:
		return nil, runner.E(runner.ErrProfileDenied, "probe query", fmt.Errorf("unsupported query kind %q", kind))
	}
}

func encode(value any) string {
	body, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(body)
}

func stringParam(params map[string]any, key string) string {
	if params == nil {
		return ""
	}
	if value, ok := params[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func intParam(params map[string]any, key string) int {
	if params == nil {
		return 0
	}
	switch value := params[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		parsed, _ := value.Int64()
		return int(parsed)
	case string:
		parsed, _ := strconv.Atoi(strings.TrimSpace(value))
		return parsed
	default:
		return 0
	}
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
