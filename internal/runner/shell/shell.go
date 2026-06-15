package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"cmesh/internal/profile"
	"cmesh/internal/runner"
)

const Name = profile.RunnerShell

type Runner struct {
	profiles map[string]ProfileConfig
}

type ProfileConfig struct {
	Capabilities          []string
	MaxOutputBytes        int64
	EnforcementConfidence string
	ShellPolicy           ShellPolicy
}

type ShellPolicy struct {
	Mode            string
	AllowedCommands map[string][]string
	DeniedPatterns  []string
}

func New(profiles map[string]ProfileConfig) *Runner {
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
			confidence = "medium"
		}
		profiles[name] = runner.ProfileManifest{
			Claims:                append([]string(nil), prof.Capabilities...),
			NativeControls:        map[string]string{"execution": "allowlisted-argv", "shell": "allowlisted", "writes": "denied-by-policy"},
			CredentialSource:      "os-user",
			Billing:               "none",
			EnforcementConfidence: confidence,
			KnownGaps:             []string{"allowlisted diagnostics still inherit OS account read permissions"},
		}
	}
	return runner.CapabilityManifest{SchemaVersion: "runner_manifest.v1", Runner: Name, Profiles: profiles}
}

func (r *Runner) Check(profileName string) error {
	if _, ok := r.profiles[profileName]; !ok {
		return runner.E(runner.ErrProfileUnavailable, "select shell profile", fmt.Errorf("%q", profileName))
	}
	return nil
}

func (r *Runner) Run(req runner.RunRequest) (runner.Result, error) {
	profileName := strings.TrimSpace(req.Profile)
	if profileName == "" && len(r.profiles) == 1 {
		for name := range r.profiles {
			profileName = name
		}
	}
	prof, ok := r.profiles[profileName]
	if !ok {
		return runner.Result{}, runner.E(runner.ErrProfileUnavailable, "select shell profile", fmt.Errorf("%q", req.Profile))
	}
	kind := strings.TrimSpace(req.QueryKind)
	if kind == "" {
		kind = profile.CapShellReadonly
	}
	if !contains(prof.Capabilities, kind) && !contains(prof.Capabilities, profile.CapShellReadonly) {
		return runner.Result{}, runner.E(runner.ErrProfileDenied, "shell query", fmt.Errorf("profile %q does not grant %s", req.Profile, kind))
	}
	argv, err := argvFromParams(req.QueryParams)
	if err != nil {
		return shellResult(kind, nil, time.Time{}, err), err
	}
	if err := validatePolicy(prof.ShellPolicy, argv); err != nil {
		return shellResult(kind, argv, time.Time{}, err), err
	}
	execArgv := shellExecArgv(argv)
	limit := req.OutputLimit
	if prof.MaxOutputBytes > 0 && (limit <= 0 || prof.MaxOutputBytes < limit) {
		limit = prof.MaxOutputBytes
	}
	started := time.Now()
	proc, err := runner.RunProgram(firstContext(req.Context), runner.Program{
		Argv:        execArgv,
		Dir:         req.CWD,
		Timeout:     req.Timeout,
		OutputLimit: limit,
		LogDir:      req.LogDir,
		LogName:     req.TaskID,
	})
	result := runner.Result{
		Status: "done",
		Tests: runner.TestClaims{
			ClaimedPassed: false,
			Commands:      []string{strings.Join(argv, " ")},
		},
		CommandOutput: runner.CommandOutput{
			Command:   strings.Join(argv, " "),
			ExitCode:  proc.ExitCode,
			Stdout:    proc.Stdout,
			Stderr:    proc.Stderr,
			StdoutRef: proc.StdoutPath,
			StderrRef: proc.StderrPath,
			DurationS: int64(proc.Duration / time.Second),
		},
		QueryResult: &runner.QueryResult{
			Kind: kind,
			Data: map[string]any{
				"command":    strings.Join(argv, " "),
				"exit_code":  proc.ExitCode,
				"stdout":     proc.Stdout,
				"stderr":     proc.Stderr,
				"stdout_ref": proc.StdoutPath,
				"stderr_ref": proc.StderrPath,
				"allowlist":  true,
				"bounded":    true,
			},
		},
		WorkerSummaryUntrusted:  summarize(kind, proc.Stdout, proc.Stderr),
		VerificationRequired:    true,
		RecommendedOriginChecks: []string{"inspect command_output.command and confirm it is allowlisted"},
		TranscriptRef:           proc.TranscriptRef,
		RunnerExtra: map[string]any{
			"runner":     Name,
			"query_kind": kind,
			"allowlist":  true,
			"no_writes":  true,
		},
	}
	if result.CommandOutput.DurationS == 0 && !started.IsZero() {
		result.CommandOutput.DurationS = int64(time.Since(started) / time.Second)
	}
	if err != nil {
		result.Status = "failed"
		if result.CommandOutput.Stderr == "" {
			result.CommandOutput.Stderr = err.Error()
		}
		return result, err
	}
	return result, nil
}

func argvFromParams(params map[string]any) ([]string, error) {
	command := stringParam(params, "command")
	if command == "" {
		return nil, runner.E(runner.ErrProfileDenied, "shell query", fmt.Errorf("params.command is required"))
	}
	argv := []string{command}
	if args, ok := params["args"].([]any); ok {
		for _, arg := range args {
			text, ok := arg.(string)
			if !ok {
				return nil, runner.E(runner.ErrProfileDenied, "shell query", fmt.Errorf("params.args must contain strings"))
			}
			argv = append(argv, text)
		}
	}
	if args, ok := params["args"].([]string); ok {
		argv = append(argv, args...)
	}
	return argv, nil
}

func validatePolicy(policy ShellPolicy, argv []string) error {
	mode := strings.TrimSpace(policy.Mode)
	if mode == "" {
		mode = "allowlist"
	}
	if mode != "allowlist" {
		return runner.E(runner.ErrProfileDenied, "shell policy", fmt.Errorf("unsupported shell policy mode %q", mode))
	}
	if len(argv) == 0 {
		return runner.E(runner.ErrProfileDenied, "shell policy", fmt.Errorf("empty command"))
	}
	joined := " " + strings.ToLower(strings.Join(argv, " ")) + " "
	for _, pattern := range policy.DeniedPatterns {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern != "" && strings.Contains(joined, pattern) {
			return runner.E(runner.ErrProfileDenied, "shell policy", fmt.Errorf("command contains denied pattern %q", pattern))
		}
	}
	allowed := allowedCommandsForOS(policy, runtime.GOOS)
	for _, candidate := range allowed {
		if strings.EqualFold(candidate, argv[0]) {
			return validateCommandArgs(runtime.GOOS, argv)
		}
	}
	return runner.E(runner.ErrProfileDenied, "shell policy", fmt.Errorf("command %q is not allowlisted for %s", argv[0], runtime.GOOS))
}

func validateCommandArgs(goos string, argv []string) error {
	if len(argv) == 0 {
		return runner.E(runner.ErrProfileDenied, "shell policy", fmt.Errorf("empty command"))
	}
	cmd := strings.ToLower(filepath.Base(argv[0]))
	args := argv[1:]
	switch goos {
	case "windows":
		return validateWindowsArgs(cmd, args)
	case "linux":
		return validateLinuxArgs(cmd, args)
	case "darwin":
		return validateDarwinArgs(cmd, args)
	default:
		return nil
	}
}

func validateWindowsArgs(cmd string, args []string) error {
	switch cmd {
	case "get-service":
		return allowArgs(cmd, args, []string{"-name", "-displayname", "-dependentservices", "-requiredservices"})
	case "get-process":
		return allowArgs(cmd, args, []string{"-name", "-id"})
	case "get-psdrive":
		return allowArgs(cmd, args, []string{"-name", "-psprovider"})
	case "get-eventlog":
		if err := allowArgs(cmd, args, []string{"-logname", "-newest", "-source", "-entrytype"}); err != nil {
			return err
		}
		return requireBoundedNewest(cmd, args, "-newest", 200)
	default:
		return nil
	}
}

func validateLinuxArgs(cmd string, args []string) error {
	switch cmd {
	case "df":
		return allowArgs(cmd, args, []string{"-h", "-k", "-p", "--human-readable", "--portability"})
	case "uname":
		return allowArgs(cmd, args, []string{"-a", "-s", "-r", "-m", "-n"})
	case "ps":
		return allowPSArgs(cmd, args)
	case "systemctl":
		return allowSystemctlArgs(cmd, args)
	case "journalctl":
		if err := allowArgs(cmd, args, []string{"-u", "-n", "--since", "--no-pager"}); err != nil {
			return err
		}
		return requireBoundedNewest(cmd, args, "-n", 200)
	default:
		return nil
	}
}

func validateDarwinArgs(cmd string, args []string) error {
	switch cmd {
	case "df":
		return allowArgs(cmd, args, []string{"-h", "-k", "-p"})
	case "uname":
		return allowArgs(cmd, args, []string{"-a", "-s", "-r", "-m", "-n"})
	case "sysctl":
		return allowArgs(cmd, args, []string{"-n"})
	case "ps":
		return allowPSArgs(cmd, args)
	case "log":
		if len(args) == 0 || strings.ToLower(args[0]) != "show" {
			return runner.E(runner.ErrProfileDenied, "shell policy", fmt.Errorf("log only allows the show subcommand"))
		}
		if err := allowArgs(cmd, args[1:], []string{"--last", "--predicate", "--style"}); err != nil {
			return err
		}
		return nil
	default:
		return nil
	}
}

func allowArgs(cmd string, args, allowedFlags []string) error {
	allowed := map[string]struct{}{}
	for _, flag := range allowedFlags {
		allowed[strings.ToLower(flag)] = struct{}{}
	}
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		name := strings.ToLower(arg)
		if idx := strings.Index(name, "="); idx >= 0 {
			name = name[:idx]
		}
		if _, ok := allowed[name]; !ok {
			return runner.E(runner.ErrProfileDenied, "shell policy", fmt.Errorf("%s argument %q is not allowed", cmd, arg))
		}
	}
	return nil
}

func allowPSArgs(cmd string, args []string) error {
	joined := strings.ToLower(strings.Join(args, " "))
	if joined == "aux" || joined == "-ef" || joined == "-e -o pid,ppid,comm" || joined == "" {
		return nil
	}
	return runner.E(runner.ErrProfileDenied, "shell policy", fmt.Errorf("%s arguments must be empty, aux, -ef, or -e -o pid,ppid,comm", cmd))
}

func allowSystemctlArgs(cmd string, args []string) error {
	if len(args) == 0 {
		return runner.E(runner.ErrProfileDenied, "shell policy", fmt.Errorf("%s requires a read-only subcommand", cmd))
	}
	switch strings.ToLower(args[0]) {
	case "status", "is-active", "list-units", "list-unit-files":
	default:
		return runner.E(runner.ErrProfileDenied, "shell policy", fmt.Errorf("%s subcommand %q is not allowed", cmd, args[0]))
	}
	return allowArgs(cmd, args[1:], []string{"--type", "--state", "--no-pager", "--plain"})
}

func requireBoundedNewest(cmd string, args []string, flag string, max int) error {
	for i, arg := range args {
		name := strings.ToLower(arg)
		value := ""
		if strings.HasPrefix(name, strings.ToLower(flag)+"=") {
			value = strings.TrimPrefix(name, strings.ToLower(flag)+"=")
		} else if name == strings.ToLower(flag) && i+1 < len(args) {
			value = args[i+1]
		}
		if value == "" {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || n < 1 || n > max {
			return runner.E(runner.ErrProfileDenied, "shell policy", fmt.Errorf("%s %s must be 1-%d", cmd, flag, max))
		}
		return nil
	}
	return runner.E(runner.ErrProfileDenied, "shell policy", fmt.Errorf("%s requires bounded %s 1-%d", cmd, flag, max))
}

func shellExecArgv(argv []string) []string {
	if runtime.GOOS == "windows" && len(argv) > 0 && strings.HasPrefix(strings.ToLower(argv[0]), "get-") {
		out := []string{"powershell", "-NoProfile", "-Command"}
		out = append(out, argv...)
		return out
	}
	return argv
}

func allowedCommandsForOS(policy ShellPolicy, goos string) []string {
	var out []string
	if policy.AllowedCommands != nil {
		out = append(out, policy.AllowedCommands[""]...)
		out = append(out, policy.AllowedCommands[goos]...)
	}
	return out
}

func shellResult(kind string, argv []string, started time.Time, err error) runner.Result {
	command := strings.Join(argv, " ")
	if command == "" {
		command = "shell:" + kind
	}
	return runner.Result{
		Status: "failed",
		CommandOutput: runner.CommandOutput{
			Command:   command,
			ExitCode:  1,
			Stderr:    err.Error(),
			DurationS: durationSeconds(started),
		},
		QueryResult: &runner.QueryResult{
			Kind: kind,
			Data: map[string]any{"command": command, "exit_code": 1, "stderr": err.Error(), "allowlist": true, "bounded": true},
		},
		WorkerSummaryUntrusted: err.Error(),
		VerificationRequired:   true,
		RunnerExtra: map[string]any{
			"runner":     Name,
			"query_kind": kind,
			"allowlist":  true,
			"no_writes":  true,
		},
	}
}

func summarize(kind, stdout, stderr string) string {
	body, _ := json.Marshal(map[string]string{
		"kind":   kind,
		"stdout": stdout,
		"stderr": stderr,
	})
	return string(body)
}

func firstContext(ctx context.Context) context.Context {
	if ctx != nil {
		return ctx
	}
	return context.Background()
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

func durationSeconds(started time.Time) int64 {
	if started.IsZero() {
		return 0
	}
	return int64(time.Since(started) / time.Second)
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
