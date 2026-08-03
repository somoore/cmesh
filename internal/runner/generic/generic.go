// Package generic runs any CLI coding agent as a cmesh runner from
// configuration alone, so joining the mesh does not require a bespoke adapter
// per vendor.
//
// The built-in adapters exist because Claude Code and Codex each have argv
// quirks worth encoding carefully. What they actually enforce, though, is
// small and portable:
//
//   - the prompt goes on stdin, never argv;
//   - profile authority (writes, shell) maps to whatever flags the CLI uses;
//   - output is parsed if it is structured and passed through raw if not;
//   - the manifest declares capabilities honestly, including its gaps.
//
// This adapter takes those four as configuration. A runner declared here is not
// less trustworthy than a built-in one — it is less *known*, which is why a
// manifest is required and `cmesh doctor runner` verifies behavior rather than
// taking the declaration's word for it.
package generic

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"cmesh/internal/runner"
)

// Name is the adapter's registry name. Multiple generic runners may exist under
// operator-chosen names; each carries its own Config.
const Name = "generic"

// Config describes one configured CLI agent.
type Config struct {
	// RunnerName is the name this runner is registered under, so several
	// generic runners can coexist.
	RunnerName string
	// Command is the argv prefix. The prompt is never appended to it.
	Command        []string
	DefaultProfile string
	Profiles       map[string]ProfileConfig
	// Manifest is the operator-supplied capability declaration. It is required:
	// an undeclared runner would be claiming capabilities cmesh cannot describe.
	Manifest *runner.CapabilityManifest
}

// ProfileConfig maps one cmesh profile onto this CLI's flags.
type ProfileConfig struct {
	Claims []string
	// Args are appended for this profile, after the command prefix. This is
	// where a CLI's own sandbox/permission flags go.
	Args []string
	// PromptArgs are appended last, immediately before the prompt is written to
	// stdin — for CLIs that need a trailing marker such as "-" to read stdin.
	PromptArgs []string
	Env        map[string]string
	// StructuredOutput reports that this CLI emits JSON on stdout, so cmesh
	// should parse a summary and usage from it. When false the raw output
	// becomes the summary.
	StructuredOutput bool
	CredentialSource string
	Billing          string
	KnownGaps        []string
}

// Runner adapts a configured CLI agent to the cmesh runner contract.
type Runner struct {
	cfg Config
}

// New validates a generic runner configuration.
//
// The validation is the point of this constructor: a misconfigured third-party
// runner should fail at daemon startup with a clear message, not at task time
// with an opaque one.
func New(cfg Config) (*Runner, error) {
	if strings.TrimSpace(cfg.RunnerName) == "" {
		return nil, errors.New("generic runner requires a name")
	}
	if len(cfg.Command) == 0 || strings.TrimSpace(cfg.Command[0]) == "" {
		return nil, fmt.Errorf("runners.%s.command is required", cfg.RunnerName)
	}
	if cfg.Manifest == nil {
		return nil, fmt.Errorf("runners.%s requires manifest_path: a generic runner must declare what it enforces, including its gaps", cfg.RunnerName)
	}
	if len(cfg.Profiles) == 0 {
		return nil, fmt.Errorf("runners.%s has no profiles", cfg.RunnerName)
	}
	// A generic runner spawns a third-party process cmesh does not control, so
	// it can never claim the confidence reserved for built-in deterministic
	// probes. ValidateSemantics enforces this for config; repeat it here so a
	// manifest file cannot smuggle the claim in.
	for name, profile := range cfg.Manifest.Profiles {
		if strings.EqualFold(profile.EnforcementConfidence, "high") {
			return nil, fmt.Errorf("runners.%s profile %q claims high enforcement confidence, which only the built-in probe runner may claim", cfg.RunnerName, name)
		}
	}
	return &Runner{cfg: cfg}, nil
}

func (r *Runner) Name() string { return r.cfg.RunnerName }

func (r *Runner) Manifest() runner.CapabilityManifest { return *r.cfg.Manifest }

// Check reports whether a profile can run: it must be configured, and the
// command must resolve.
func (r *Runner) Check(profile string) error {
	if _, err := r.profile(profile); err != nil {
		return err
	}
	if _, err := exec.LookPath(r.cfg.Command[0]); err != nil {
		return runner.E(runner.ErrRunnerUnavailable, "resolve command", err)
	}
	return nil
}

func (r *Runner) profile(name string) (ProfileConfig, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = r.cfg.DefaultProfile
	}
	profile, ok := r.cfg.Profiles[name]
	if !ok {
		return ProfileConfig{}, runner.E(runner.ErrProfileUnavailable, "resolve profile",
			fmt.Errorf("profile %q is not configured for runner %s", name, r.cfg.RunnerName))
	}
	return profile, nil
}

// Argv builds the command for a profile. It is exported so doctor can inspect
// what would run without launching anything.
//
// The prompt is deliberately absent: it is written to stdin by Run. A generic
// runner cannot opt into argv prompt delivery, because cmesh cannot verify a
// third-party CLI's argument handling.
func (r *Runner) Argv(profile string) ([]string, error) {
	prof, err := r.profile(profile)
	if err != nil {
		return nil, err
	}
	argv := append([]string(nil), r.cfg.Command...)
	argv = append(argv, prof.Args...)
	argv = append(argv, prof.PromptArgs...)
	return argv, nil
}

func (r *Runner) Run(req runner.RunRequest) (runner.Result, error) {
	prof, err := r.profile(req.Profile)
	if err != nil {
		return runner.Result{}, err
	}
	argv, err := r.Argv(req.Profile)
	if err != nil {
		return runner.Result{}, err
	}
	env := map[string]string{}
	for key, value := range prof.Env {
		env[key] = value
	}
	for key, value := range req.Env {
		env[key] = value
	}

	proc, runErr := runner.RunProgram(req.Context, runner.Program{
		Argv: argv,
		Dir:  req.CWD,
		Env:  env,
		// The prompt travels on stdin. Every cmesh runner does this, and a
		// third-party CLI is exactly the case where argv delivery would be
		// least verifiable.
		Stdin:       req.Prompt,
		Timeout:     req.Timeout,
		OutputLimit: req.OutputLimit,
		LogDir:      req.LogDir,
		LogName:     req.TaskID,
	})
	result := runner.ResultFromProcess(r.cfg.RunnerName, proc, argv, req.Prompt)
	if !prof.StructuredOutput {
		// This CLI does not promise JSON, so do not let an accidental
		// JSON-looking line be read as a structured summary: the raw output is
		// the summary, and the captured envelope remains the evidence.
		result.WorkerSummaryUntrusted = strings.TrimSpace(proc.Combined)
		if len(result.WorkerSummaryUntrusted) > runner.MaxSummaryBytes {
			result.WorkerSummaryUntrusted = result.WorkerSummaryUntrusted[:runner.MaxSummaryBytes]
		}
	}
	if runErr != nil {
		result.Status = "failed"
		return result, runErr
	}
	return result, nil
}
