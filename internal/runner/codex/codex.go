package codex

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"cmesh/internal/runner"
)

const Name = "codex"

type Config struct {
	Binary         string
	Command        []string
	DefaultProfile string
	Profiles       map[string]ProfileConfig
}

type ProfileConfig struct {
	Claims     []string
	Sandbox    string
	Approval   string
	Network    string
	Model      string
	Config     map[string]string
	ExtraArgs  []string
	Env        map[string]string
	Confidence string
	KnownGaps  []string
}

type Runner struct {
	cfg Config
}

func New(cfg Config) *Runner {
	if cfg.Binary == "" {
		cfg.Binary = "codex"
	}
	if cfg.DefaultProfile == "" {
		cfg.DefaultProfile = "dev"
	}
	if len(cfg.Profiles) == 0 {
		cfg.Profiles = defaultProfiles()
	}
	return &Runner{cfg: cfg}
}

func (r *Runner) Name() string {
	return Name
}

func (r *Runner) Manifest() runner.CapabilityManifest {
	profiles := make(map[string]runner.ProfileManifest, len(r.cfg.Profiles))
	for name, profile := range r.cfg.Profiles {
		controls := map[string]string{}
		if profile.Sandbox != "" {
			controls["sandbox"] = profile.Sandbox
		}
		if profile.Approval != "" {
			controls["approval"] = profile.Approval
		}
		if profile.Network != "" {
			controls["network"] = profile.Network
		}
		confidence := profile.Confidence
		if confidence == "" {
			confidence = "medium"
		}
		profiles[name] = runner.ProfileManifest{
			Claims:                append([]string(nil), profile.Claims...),
			NativeFlags:           nativeFlags(profile),
			NativeControls:        controls,
			CredentialSource:      "codex login or configured API credentials",
			Billing:               "codex account",
			EnforcementConfidence: confidence,
			KnownGaps:             append([]string(nil), profile.KnownGaps...),
			// The agent receives queued messages when it calls task_ask through its
			// task-scoped MCP gateway. Live stdin streaming is not wired yet.
			MidTaskInput: "checkpoint",
		}
	}
	return runner.CapabilityManifest{
		SchemaVersion: "runner_manifest.v1",
		Runner:        Name,
		Profiles:      profiles,
	}
}

func (r *Runner) Check(profileName string) error {
	if _, err := r.profile(profileName); err != nil {
		return err
	}
	cmd := r.binaryForLookup()
	if _, err := exec.LookPath(cmd); err != nil {
		return runner.E(runner.ErrRunnerUnavailable, "resolve codex command", err)
	}
	return nil
}

func (r *Runner) Run(req runner.RunRequest) (runner.Result, error) {
	profile, err := r.profile(req.Profile)
	if err != nil {
		return runner.Result{}, err
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return runner.Result{}, runner.E(runner.ErrWorkerFailed, "validate prompt", errors.New("empty prompt"))
	}
	if req.Metadata.ResumeSessionID != "" {
		return runner.Result{}, runner.E(runner.ErrWorkerFailed, "resume codex session", errors.New("codex runner does not support resume_session_id"))
	}
	argv := r.argv(req, profile)
	env := merge(profile.Env, req.Env)
	ctx := req.Context
	if ctx == nil {
		ctx = context.Background()
	}
	proc, err := runner.RunProgram(ctx, runner.Program{
		Argv:        argv,
		Dir:         req.CWD,
		Env:         env,
		Stdin:       req.Prompt,
		Timeout:     req.Timeout,
		OutputLimit: req.OutputLimit,
		LogDir:      req.LogDir,
		LogName:     req.TaskID + ".codex",
	})
	result := resultFromProcess(req, proc, argv)
	if err != nil {
		return result, err
	}
	return result, nil
}

func (r *Runner) TaskScopedArgv(profileName, cwd, mcpURL string) ([]string, error) {
	profile, err := r.profile(profileName)
	if err != nil {
		return nil, err
	}
	argv := r.argv(runner.RunRequest{
		Profile: profileName,
		CWD:     cwd,
		Metadata: runner.TaskMetadata{
			MCPURL: mcpURL,
		},
	}, profile)
	return append([]string(nil), argv...), nil
}

func (r *Runner) profile(name string) (ProfileConfig, error) {
	if name == "" {
		name = r.cfg.DefaultProfile
	}
	profile, ok := r.cfg.Profiles[name]
	if !ok {
		return ProfileConfig{}, runner.E(runner.ErrProfileUnavailable, "select codex profile", fmt.Errorf("%q", name))
	}
	return profile, nil
}

func (r *Runner) argv(req runner.RunRequest, profile ProfileConfig) []string {
	argv := normalizeExecArgs(r.commandPrefix())
	extraArgs := sanitizeExtraArgs(profile.ExtraArgs, profile)
	if profile.Approval != "" {
		argv = setOptionBeforeExec(argv, "--ask-for-approval", profile.Approval)
	}
	if !commandIncludesExec(argv) {
		argv = append(argv, "exec")
	}
	if profile.Sandbox != "" {
		argv = setOptionBeforeExec(argv, "--sandbox", profile.Sandbox)
	}
	argv = moveExecBooleanFlagsAfterExec(argv, "--skip-git-repo-check", "--ignore-user-config", "--ignore-rules")
	if !hasArg(argv, "--skip-git-repo-check") && !hasArg(extraArgs, "--skip-git-repo-check") {
		argv = append(argv, "--skip-git-repo-check")
	}
	if req.Metadata.MCPURL != "" {
		if !hasArg(argv, "--ignore-user-config") && !hasArg(extraArgs, "--ignore-user-config") {
			argv = append(argv, "--ignore-user-config")
		}
		if !hasArg(argv, "--ignore-rules") && !hasArg(extraArgs, "--ignore-rules") {
			argv = append(argv, "--ignore-rules")
		}
	}
	if profile.Model != "" {
		argv = append(argv, "--model", profile.Model)
	}
	keys := make([]string, 0, len(profile.Config))
	for key := range profile.Config {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if profile.Sandbox != "" && key == "sandbox_mode" {
			continue
		}
		argv = append(argv, "-c", key+"="+profile.Config[key])
	}
	if profile.Sandbox != "" {
		argv = append(argv, "-c", "sandbox_mode=\""+tomlEscape(profile.Sandbox)+"\"")
	}
	if req.Metadata.MCPURL != "" {
		argv = append(argv,
			"-c", "mcp_servers.cmesh.url=\""+tomlEscape(req.Metadata.MCPURL)+"\"",
			"-c", "mcp_servers.cmesh.bearer_token_env_var=\"CMESH_TASK_TOKEN\"",
			"-c", "mcp_servers.cmesh.startup_timeout_sec=120",
		)
	}
	if req.CWD != "" {
		argv = append(argv, "-C", req.CWD)
	}
	argv = append(argv, extraArgs...)
	argv = append(argv, "-")
	return argv
}

func normalizeExecArgs(argv []string) []string {
	codexIndex := codexCommandIndex(argv)
	if codexIndex < 0 {
		return argv
	}
	execIndex := execIndexAfter(argv, codexIndex)
	normalizeEnd := len(argv)
	var trailing []string
	if execIndex >= 0 {
		normalizeEnd = execIndex
		trailing = argv[execIndex+1:]
	}
	beforeExec := append([]string(nil), argv[:codexIndex+1]...)
	afterExec := make([]string, 0, len(argv))
	for i := codexIndex + 1; i < normalizeEnd; i++ {
		arg := argv[i]
		if isCodexExecOnlyFlag(arg) {
			afterExec = append(afterExec, arg)
			if codexExecOnlyFlagTakesValue(arg) && i+1 < normalizeEnd {
				i++
				afterExec = append(afterExec, argv[i])
			}
		} else {
			beforeExec = append(beforeExec, arg)
		}
	}
	out := make([]string, 0, len(argv)+1)
	out = append(out, beforeExec...)
	out = append(out, "exec")
	out = append(out, afterExec...)
	out = append(out, trailing...)
	return out
}

func codexCommandIndex(argv []string) int {
	for i, arg := range argv {
		if isCodexCommand(arg) {
			return i
		}
	}
	return -1
}

func isCodexCommand(command string) bool {
	command = strings.ToLower(strings.TrimSpace(command))
	command = strings.TrimSuffix(command, ".exe")
	command = strings.TrimSuffix(command, ".cmd")
	return command == "codex" || strings.HasSuffix(command, `/codex`) || strings.HasSuffix(command, `\codex`)
}

func isCodexExecOnlyFlag(arg string) bool {
	switch codexFlagName(arg) {
	case "--skip-git-repo-check",
		"--ephemeral",
		"--ignore-user-config",
		"--ignore-rules",
		"--output-schema",
		"-o",
		"--color",
		"--json",
		"--experimental-json",
		"--full-auto",
		"--output-last-message":
		return true
	default:
		return false
	}
}

func codexExecOnlyFlagTakesValue(arg string) bool {
	if strings.Contains(arg, "=") {
		return false
	}
	switch codexFlagName(arg) {
	case "--output-schema", "--color", "--output-last-message", "-o":
		return true
	default:
		return false
	}
}

func codexFlagName(arg string) string {
	name, _, _ := strings.Cut(arg, "=")
	return name
}

func insertBeforeExec(argv []string, values ...string) []string {
	index := execIndex(argv)
	if index < 0 {
		return append(argv, values...)
	}
	out := make([]string, 0, len(argv)+len(values))
	out = append(out, argv[:index]...)
	out = append(out, values...)
	out = append(out, argv[index:]...)
	return out
}

func setOptionBeforeExec(argv []string, flag, value string) []string {
	argv = removeOption(argv, flag)
	return insertBeforeExec(argv, flag, value)
}

func removeOption(argv []string, flag string) []string {
	out := make([]string, 0, len(argv))
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if arg == flag {
			if i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "-") {
				i++
			}
			continue
		}
		if strings.HasPrefix(arg, flag+"=") {
			continue
		}
		out = append(out, arg)
	}
	return out
}

func sanitizeExtraArgs(args []string, profile ProfileConfig) []string {
	out := append([]string(nil), args...)
	if profile.Approval != "" {
		out = removeOption(out, "--ask-for-approval")
	}
	if profile.Sandbox != "" {
		out = removeOption(out, "--sandbox")
		out = removeConfigKey(out, "sandbox_mode")
	}
	return out
}

func removeConfigKey(argv []string, key string) []string {
	out := make([]string, 0, len(argv))
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch arg {
		case "-c", "--config":
			if i+1 < len(argv) && configArgKey(argv[i+1]) == key {
				i++
				continue
			}
		default:
			if strings.HasPrefix(arg, "-c=") && configArgKey(strings.TrimPrefix(arg, "-c=")) == key {
				continue
			}
			if strings.HasPrefix(arg, "--config=") && configArgKey(strings.TrimPrefix(arg, "--config=")) == key {
				continue
			}
		}
		out = append(out, arg)
	}
	return out
}

func configArgKey(arg string) string {
	if idx := strings.Index(arg, "="); idx >= 0 {
		return strings.TrimSpace(arg[:idx])
	}
	return strings.TrimSpace(arg)
}

func (r *Runner) commandPrefix() []string {
	if len(r.cfg.Command) > 0 {
		return append([]string(nil), r.cfg.Command...)
	}
	return []string{r.cfg.Binary}
}

func commandIncludesExec(argv []string) bool {
	return execIndex(argv) >= 0
}

func moveExecBooleanFlagsAfterExec(argv []string, flags ...string) []string {
	execAt := execIndex(argv)
	if execAt < 0 {
		return argv
	}
	execFlags := map[string]struct{}{}
	for _, flag := range flags {
		execFlags[flag] = struct{}{}
	}
	var moved []string
	out := make([]string, 0, len(argv))
	for i, arg := range argv {
		if i < execAt {
			if _, ok := execFlags[arg]; ok {
				if !hasArg(moved, arg) {
					moved = append(moved, arg)
				}
				continue
			}
		}
		out = append(out, arg)
	}
	if len(moved) == 0 {
		return argv
	}
	execAt = execIndex(out)
	var insert []string
	for _, flag := range moved {
		if !hasArgAfter(out, flag, execAt) && !hasArg(insert, flag) {
			insert = append(insert, flag)
		}
	}
	if len(insert) == 0 {
		return out
	}
	next := make([]string, 0, len(out)+len(insert))
	next = append(next, out[:execAt+1]...)
	next = append(next, insert...)
	next = append(next, out[execAt+1:]...)
	return next
}

func hasArg(argv []string, needle string) bool {
	for _, arg := range argv {
		if arg == needle {
			return true
		}
	}
	return false
}

func hasArgAfter(argv []string, needle string, index int) bool {
	for i, arg := range argv {
		if i <= index {
			continue
		}
		if arg == needle {
			return true
		}
	}
	return false
}

func execIndex(argv []string) int {
	return execIndexAfter(argv, 0)
}

func execIndexAfter(argv []string, start int) int {
	for i := start + 1; i < len(argv); i++ {
		arg := argv[i]
		if arg == "exec" {
			return i
		}
	}
	return -1
}

func (r *Runner) binaryForLookup() string {
	if len(r.cfg.Command) > 0 {
		return r.cfg.Command[0]
	}
	return r.cfg.Binary
}

func nativeFlags(profile ProfileConfig) []string {
	var flags []string
	if profile.Approval != "" {
		flags = append(flags, "--ask-for-approval", profile.Approval)
	}
	if profile.Sandbox != "" {
		flags = append(flags, "--sandbox", profile.Sandbox)
	}
	if profile.Model != "" {
		flags = append(flags, "--model", profile.Model)
	}
	flags = append(flags, profile.ExtraArgs...)
	return flags
}

func resultFromProcess(req runner.RunRequest, proc runner.CompletedProcess, argv []string) runner.Result {
	return runner.ResultFromProcess(Name, proc, argv, req.Prompt)
}

// PreviewTaskScopedArgv builds the task-scoped argv shape cmesh uses when it
// injects a per-task MCP server. Doctor uses this to validate flag placement
// without launching a worker.
func PreviewTaskScopedArgv(command []string, profile ProfileConfig) []string {
	run := New(Config{
		Command:        command,
		DefaultProfile: "doctor-preview",
		Profiles: map[string]ProfileConfig{
			"doctor-preview": profile,
		},
	})
	return run.argv(runner.RunRequest{
		Profile: "doctor-preview",
		Metadata: runner.TaskMetadata{
			MCPURL: "http://127.0.0.1:1/mcp",
		},
	}, profile)
}

// PreviewExecHelpArgv builds the configured Codex command prefix into a
// non-interactive help probe without launching a worker.
func PreviewExecHelpArgv(command []string) []string {
	if len(command) == 0 {
		return nil
	}
	argv := normalizeExecArgs(append([]string(nil), command...))
	if !commandIncludesExec(argv) {
		argv = append(argv, "exec")
	}
	return append(argv, "--help")
}

func tomlEscape(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return replacer.Replace(value)
}

func defaultProfiles() map[string]ProfileConfig {
	return map[string]ProfileConfig{
		"readonly": {
			Claims:     []string{"read_files"},
			Sandbox:    "read-only",
			Approval:   "never",
			Network:    "disabled_or_limited",
			Confidence: "medium",
			KnownGaps: []string{
				"approval and sandbox are separate axes",
				"network behavior depends on Codex CLI configuration and wrapper",
			},
		},
		"dev": {
			Claims:     []string{"read_files", "edit_repo", "execute_repo_commands"},
			Sandbox:    "workspace-write",
			Approval:   "never",
			Network:    "disabled_or_limited",
			Confidence: "medium",
			KnownGaps: []string{
				"approval prompts are not proxied to the origin session",
				"network behavior depends on Codex CLI configuration and wrapper",
			},
		},
	}
}

func merge(a, b map[string]string) map[string]string {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make(map[string]string, len(a)+len(b))
	for key, value := range a {
		out[key] = value
	}
	for key, value := range b {
		out[key] = value
	}
	return out
}
