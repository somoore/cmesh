package claudecode

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"cmesh/internal/runner"
)

const Name = "claude-code"

type Config struct {
	Command        []string
	DefaultProfile string
	Profiles       map[string]ProfileConfig
}

type ProfileConfig struct {
	Claims           []string
	Bare             bool
	PermissionMode   string
	AllowedTools     []string
	DisallowedTools  []string
	OutputFormat     string
	APIKeyHelper     string
	ExtraArgs        []string
	Env              map[string]string
	CredentialSource string
	Billing          string
	Confidence       string
	KnownGaps        []string
}

type Runner struct {
	cfg Config
}

func New(cfg Config) *Runner {
	if len(cfg.Command) == 0 {
		cfg.Command = []string{"claude", "-p"}
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
		flags := buildNativeFlags(profile)
		credentialSource := profile.CredentialSource
		if credentialSource == "" && profile.Bare {
			credentialSource = "ANTHROPIC_API_KEY|apiKeyHelper"
		}
		billing := profile.Billing
		if billing == "" && profile.Bare {
			billing = "api"
		} else if billing == "" {
			billing = "subscription"
		}
		confidence := profile.Confidence
		if confidence == "" {
			confidence = "medium"
		}
		profiles[name] = runner.ProfileManifest{
			Claims:                append([]string(nil), profile.Claims...),
			NativeFlags:           flags,
			CredentialSource:      credentialSource,
			Billing:               billing,
			EnforcementConfidence: confidence,
			KnownGaps:             append([]string(nil), profile.KnownGaps...),
			Bare:                  profile.Bare,
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
	profile, err := r.profile(profileName)
	if err != nil {
		return err
	}
	if _, err := exec.LookPath(r.cfg.Command[0]); err != nil {
		return runner.E(runner.ErrRunnerUnavailable, "resolve claude command", err)
	}
	if profile.Bare {
		if err := checkBareCredentials(profile, nil); err != nil {
			return err
		}
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
	if profile.Bare {
		if err := checkBareCredentials(profile, req.Env); err != nil {
			return runner.Result{}, err
		}
	}
	argv := r.argv(req, profile)
	if req.Metadata.ResumeSessionID != "" {
		argv = append(argv, "--resume", req.Metadata.ResumeSessionID)
	}
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
		LogName:     req.TaskID + ".claude-code",
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
		return ProfileConfig{}, runner.E(runner.ErrProfileUnavailable, "select claude profile", fmt.Errorf("%q", name))
	}
	return profile, nil
}

func (r *Runner) argv(req runner.RunRequest, profile ProfileConfig) []string {
	argv := append([]string(nil), r.cfg.Command...)
	argv = append(argv, argsForProfile(profile)...)
	if req.Metadata.MCPConfigFile != "" {
		argv = append(argv, "--mcp-config", req.Metadata.MCPConfigFile, "--strict-mcp-config")
	}
	return argv
}

func argsForProfile(profile ProfileConfig) []string {
	var args []string
	if profile.Bare {
		args = append(args, "--bare")
	}
	if profile.PermissionMode != "" {
		args = append(args, "--permission-mode", profile.PermissionMode)
	}
	if profile.OutputFormat != "" {
		args = append(args, "--output-format", profile.OutputFormat)
	}
	if len(profile.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(profile.AllowedTools, ","))
	}
	if len(profile.DisallowedTools) > 0 {
		args = append(args, "--disallowedTools", strings.Join(profile.DisallowedTools, ","))
	}
	args = append(args, profile.ExtraArgs...)
	return args
}

func buildNativeFlags(profile ProfileConfig) []string {
	return argsForProfile(profile)
}

func checkBareCredentials(profile ProfileConfig, env map[string]string) error {
	if envValue(env, "ANTHROPIC_API_KEY") != "" || os.Getenv("ANTHROPIC_API_KEY") != "" {
		return nil
	}
	if profile.APIKeyHelper == "" {
		return runner.E(runner.ErrProfileUnavailable, "check bare credentials", errors.New("bare profile requires ANTHROPIC_API_KEY or apiKeyHelper"))
	}
	if strings.ContainsAny(profile.APIKeyHelper, `/\`) {
		info, err := os.Stat(profile.APIKeyHelper)
		if err != nil {
			return runner.E(runner.ErrProfileUnavailable, "check apiKeyHelper", err)
		}
		if info.IsDir() {
			return runner.E(runner.ErrProfileUnavailable, "check apiKeyHelper", fmt.Errorf("%s is a directory", profile.APIKeyHelper))
		}
		return nil
	}
	if _, err := exec.LookPath(profile.APIKeyHelper); err != nil {
		return runner.E(runner.ErrProfileUnavailable, "check apiKeyHelper", err)
	}
	return nil
}

func resultFromProcess(req runner.RunRequest, proc runner.CompletedProcess, argv []string) runner.Result {
	return runner.ResultFromProcess(Name, proc, argv, req.Prompt)
}

func defaultProfiles() map[string]ProfileConfig {
	return map[string]ProfileConfig{
		"readonly": {
			Claims:           []string{"read_files"},
			Bare:             true,
			PermissionMode:   "plan",
			OutputFormat:     "json",
			CredentialSource: "ANTHROPIC_API_KEY|apiKeyHelper",
			Billing:          "api",
			Confidence:       "medium",
			KnownGaps: []string{
				"bare mode loses CLAUDE.md and ambient project guidance",
				"bare mode requires API credentials and may bill through the API route",
			},
		},
		"dev": {
			Claims:       []string{"read_files", "edit_repo", "execute_repo_commands"},
			OutputFormat: "json",
			Billing:      "subscription",
			Confidence:   "medium",
			KnownGaps: []string{
				"non-bare runs inherit ambient Claude Code environment",
				"native tool permissions are not identical across harnesses",
			},
		},
	}
}

func envValue(env map[string]string, key string) string {
	if env == nil {
		return ""
	}
	return env[key]
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
