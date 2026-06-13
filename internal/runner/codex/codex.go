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
	argv := r.commandPrefix()
	if profile.Approval != "" {
		argv = insertBeforeExec(argv, "--ask-for-approval", profile.Approval)
	}
	if !commandIncludesExec(argv) {
		argv = append(argv, "exec")
	}
	if profile.Sandbox != "" {
		argv = append(argv, "--sandbox", profile.Sandbox)
	}
	if !hasArg(argv, "--skip-git-repo-check") && !hasArg(profile.ExtraArgs, "--skip-git-repo-check") {
		argv = append(argv, "--skip-git-repo-check")
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
		argv = append(argv, "-c", key+"="+profile.Config[key])
	}
	if req.CWD != "" {
		argv = append(argv, "-C", req.CWD)
	}
	argv = append(argv, profile.ExtraArgs...)
	argv = append(argv, "-")
	return argv
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

func (r *Runner) commandPrefix() []string {
	if len(r.cfg.Command) > 0 {
		return append([]string(nil), r.cfg.Command...)
	}
	return []string{r.cfg.Binary}
}

func commandIncludesExec(argv []string) bool {
	return execIndex(argv) >= 0
}

func hasArg(argv []string, needle string) bool {
	for _, arg := range argv {
		if arg == needle {
			return true
		}
	}
	return false
}

func execIndex(argv []string) int {
	for i, arg := range argv[1:] {
		if arg == "exec" {
			return i + 1
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
