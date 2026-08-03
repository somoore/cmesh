package generic

import (
	"context"
	"strings"
	"testing"

	"cmesh/internal/runner"
	"cmesh/internal/runner/runnertest"
)

func testManifest() *runner.CapabilityManifest {
	return &runner.CapabilityManifest{
		SchemaVersion: "runner_manifest.v1",
		Runner:        "acme-agent",
		Profiles: map[string]runner.ProfileManifest{
			"repo-dev": {
				Claims:                []string{"read_files", "edit_repo"},
				EnforcementConfidence: "medium",
				KnownGaps:             []string{"third-party CLI; enforcement depends on its own flags"},
			},
		},
	}
}

// A generic runner must declare what it enforces. Every way the declaration can
// be missing or overclaiming is refused at construction, so a misconfigured
// third-party runner fails at startup with a clear message rather than at task
// time with an opaque one.
func TestNewRefusesUndeclaredOrOverclaimingRunners(t *testing.T) {
	base := func() Config {
		return Config{
			RunnerName:     "acme-agent",
			Command:        []string{"acme"},
			DefaultProfile: "repo-dev",
			Profiles:       map[string]ProfileConfig{"repo-dev": {Claims: []string{"read_files"}}},
			Manifest:       testManifest(),
		}
	}
	if _, err := New(base()); err != nil {
		t.Fatalf("a fully declared runner should construct: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"no name", func(c *Config) { c.RunnerName = "" }, "requires a name"},
		{"no command", func(c *Config) { c.Command = nil }, "command is required"},
		{"no manifest", func(c *Config) { c.Manifest = nil }, "manifest_path"},
		{"no profiles", func(c *Config) { c.Profiles = nil }, "no profiles"},
		{"overclaims confidence", func(c *Config) {
			m := testManifest()
			p := m.Profiles["repo-dev"]
			p.EnforcementConfidence = "high"
			m.Profiles["repo-dev"] = p
			c.Manifest = m
		}, "only the built-in probe runner"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			tc.mutate(&cfg)
			_, err := New(cfg)
			if err == nil {
				t.Fatalf("expected refusal for %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// The prompt travels on stdin, never argv. This is the invariant every cmesh
// adapter holds, and a third-party CLI is exactly where argv delivery would be
// least verifiable -- so it is enforced, not requested.
func TestGenericRunnerDeliversPromptOnStdin(t *testing.T) {
	run, err := New(Config{
		RunnerName:     "acme-agent",
		Command:        []string{"sh", "-c", "cat"},
		DefaultProfile: "repo-dev",
		Profiles:       map[string]ProfileConfig{"repo-dev": {Claims: []string{"read_files"}}},
		Manifest:       testManifest(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	runnertest.AssertPromptViaStdin(t, run, "tsk_stdin", "the prompt must arrive on stdin", "repo-dev")
}

// A profile's flags reach argv in a stable order, and the prompt never does.
func TestArgvOrderAndPromptAbsence(t *testing.T) {
	run, err := New(Config{
		RunnerName:     "acme-agent",
		Command:        []string{"acme", "run"},
		DefaultProfile: "repo-dev",
		Profiles: map[string]ProfileConfig{
			"repo-dev": {Args: []string{"--sandbox", "workspace"}, PromptArgs: []string{"-"}},
		},
		Manifest: testManifest(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	argv, err := run.Argv("repo-dev")
	if err != nil {
		t.Fatalf("Argv: %v", err)
	}
	want := []string{"acme", "run", "--sandbox", "workspace", "-"}
	if strings.Join(argv, " ") != strings.Join(want, " ") {
		t.Fatalf("argv = %v, want %v", argv, want)
	}

	if _, err := run.Argv("not-configured"); err == nil {
		t.Fatal("an unconfigured profile must be refused")
	}
}

// An unstructured runner's raw output becomes the summary: cmesh must not read
// an accidental JSON-looking line as a structured claim.
func TestUnstructuredOutputIsNotParsedAsStructured(t *testing.T) {
	run, err := New(Config{
		RunnerName:     "acme-agent",
		Command:        []string{"sh", "-c", `printf '{"result":"parsed as structured"}'`},
		DefaultProfile: "repo-dev",
		Profiles:       map[string]ProfileConfig{"repo-dev": {StructuredOutput: false}},
		Manifest:       testManifest(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	result, err := run.Run(runner.RunRequest{
		Context: context.Background(),
		TaskID:  "tsk_generic",
		Prompt:  "hello",
		CWD:     t.TempDir(),
		Profile: "repo-dev",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.WorkerSummaryUntrusted != `{"result":"parsed as structured"}` {
		t.Fatalf("summary = %q, want the raw output verbatim", result.WorkerSummaryUntrusted)
	}
	if result.Tests.ClaimedPassed {
		t.Fatal("no runner may claim tests passed")
	}
}
