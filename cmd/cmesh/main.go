package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"cmesh/internal/audit"
	"cmesh/internal/config"
	"cmesh/internal/doctor"
	"cmesh/internal/gitx"
	tsid "cmesh/internal/identity/tailscale"
	"cmesh/internal/mcp/gateway"
	meshrpc "cmesh/internal/mesh/rpc"
	"cmesh/internal/policy"
	"cmesh/internal/runner"
	"cmesh/internal/runner/claudecode"
	"cmesh/internal/runner/codex"
	"cmesh/internal/store"
	"cmesh/internal/task"
	"cmesh/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	switch args[0] {
	case "up":
		return cmdUp(args[1:])
	case "doctor":
		return cmdDoctor(args[1:])
	case "status":
		return cmdStatus(args[1:])
	case "log":
		return cmdLog(args[1:])
	case "connect":
		return cmdConnect(args[1:])
	case "hello":
		return cmdHello(args[1:])
	case "version":
		return cmdVersion(args[1:])
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func cmdUp(args []string) error {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	configPath := fs.String("config", config.DefaultPath(), "path to cmesh config")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := store.OpenContext(ctx, cfg.Store.Path)
	if err != nil {
		return err
	}
	defer st.Close()
	auditWriter := audit.NewWriter(cfg.Audit.Path, cfg.Node.ID)
	policyEngine := policy.New(cfg)
	registry, err := buildRunners(cfg)
	if err != nil {
		return err
	}
	if err := failClosedRestart(ctx, cfg.Node.DataDir, st, auditWriter); err != nil {
		return err
	}
	warnCleanup(cleanupRetainedTaskArtifacts(ctx, cfg, st))
	resolver := tsid.NewResolver()
	mesh := &meshrpc.Server{Config: cfg, Store: st, Audit: auditWriter, Policy: policyEngine, Identity: resolver, Runners: registry}
	local := &gateway.Gateway{Config: cfg, Store: st, Audit: auditWriter, Policy: policyEngine}
	errCh := make(chan error, 2)
	go func() { errCh <- mesh.ListenAndServe(ctx) }()
	go func() { errCh <- local.ListenAndServe(ctx) }()
	go cleanupLoop(ctx, cfg, st)
	meshAddr, _ := mesh.BindAddress(ctx)
	fmt.Printf("cmesh up: mesh=%s gateway=127.0.0.1:%d\n", meshAddr, cfg.Node.MCPPort)
	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		if err != nil {
			return err
		}
		return nil
	}
}

func cmdDoctor(args []string) error {
	if len(args) == 0 || args[0] != "tailscale" {
		return fmt.Errorf("usage: cmesh doctor tailscale [-mesh-port 7778]")
	}
	fs := flag.NewFlagSet("doctor tailscale", flag.ExitOnError)
	port := fs.Int("mesh-port", config.DefaultMeshPort, "mesh RPC port")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	report := doctor.Tailscale(context.Background(), doctor.TailscaleOptions{MeshPort: *port})
	fmt.Print(report.Text())
	if !report.OK() {
		return errors.New("tailscale doctor failed")
	}
	return nil
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	configPath := fs.String("config", config.DefaultPath(), "path to cmesh config")
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.Store.Path)
	if err != nil {
		return err
	}
	defer st.Close()
	tasks, err := st.RecentTasks(context.Background(), 20)
	if err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(tasks)
	}
	if len(tasks) == 0 {
		fmt.Println("no tasks")
		return nil
	}
	for _, rec := range tasks {
		fmt.Printf("%s\t%s\t%s\t%s\t%s\n", rec.ID, rec.State, rec.Runner, rec.ProfileEffective, rec.UpdatedAt.Format(time.RFC3339))
	}
	return nil
}

func cmdLog(args []string) error {
	fs := flag.NewFlagSet("log", flag.ExitOnError)
	configPath := fs.String("config", config.DefaultPath(), "path to cmesh config")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: cmesh log <task_id>")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	taskID := fs.Arg(0)
	logDir := filepath.Join(cfg.Node.DataDir, "logs", taskID)
	files, err := filepath.Glob(filepath.Join(logDir, "*.log"))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no logs found for %s", taskID)
	}
	for _, path := range files {
		fmt.Printf("== %s ==\n", path)
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fmt.Print(string(body))
		if len(body) > 0 && body[len(body)-1] != '\n' {
			fmt.Println()
		}
	}
	return nil
}

func cmdConnect(args []string) error {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	configPath := fs.String("config", config.DefaultPath(), "path to cmesh config")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	fmt.Printf("Local cmesh gateway: http://127.0.0.1:%d\n", cfg.Node.MCPPort)
	gatewayURL := fmt.Sprintf("http://127.0.0.1:%d/mcp", cfg.Node.MCPPort)
	if claude, err := exec.LookPath("claude"); err == nil {
		cmd := exec.Command(claude, "mcp", "add", "cmesh", gatewayURL)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("register Claude Code MCP server: %w", err)
		}
		fmt.Println("Claude Code MCP registration updated.")
	} else {
		fmt.Println("Claude Code not found on PATH; register manually with:")
		fmt.Printf("  claude mcp add cmesh %s\n", gatewayURL)
	}
	fmt.Println("Codex MCP registration varies by installed CLI version; add a local HTTP MCP server named cmesh at:")
	fmt.Printf("  %s\n", gatewayURL)
	return nil
}

func cmdHello(args []string) error {
	fs := flag.NewFlagSet("hello", flag.ExitOnError)
	peerURL := fs.String("url", "", "peer base URL, e.g. http://100.x.y.z:7778")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *peerURL == "" {
		return fmt.Errorf("usage: cmesh hello -url http://100.x.y.z:7778")
	}
	resp, err := meshrpc.NewClient(*peerURL).Hello(context.Background())
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(resp)
}

func cmdVersion(args []string) error {
	fs := flag.NewFlagSet("version", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	info := map[string]string{
		"version": version.Version,
		"commit":  version.Commit,
		"date":    version.Date,
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(info)
	}
	fmt.Printf("cmesh %s (%s, %s)\n", version.Version, version.Commit, version.Date)
	return nil
}

func buildRunners(cfg *config.Config) (*runner.Registry, error) {
	reg := runner.NewRegistry()
	for name, rcfg := range cfg.Runners {
		switch name {
		case claudecode.Name:
			profiles := map[string]claudecode.ProfileConfig{}
			for pname, pcfg := range cfg.Profiles {
				if pcfg.Runner != name {
					continue
				}
				profiles[pname] = claudecode.ProfileConfig{Claims: pcfg.Claims, Bare: pcfg.Bare, OutputFormat: "json"}
			}
			run := claudecode.New(claudecode.Config{Command: rcfg.Command, DefaultProfile: rcfg.DefaultProfile, Profiles: profiles})
			wrapped, err := applyManifestPath(run, rcfg.ManifestPath)
			if err != nil {
				return nil, err
			}
			if err := reg.Register(wrapped); err != nil {
				return nil, err
			}
		case codex.Name:
			profiles := map[string]codex.ProfileConfig{}
			for pname, pcfg := range cfg.Profiles {
				if pcfg.Runner != name {
					continue
				}
				network := "disabled_or_limited"
				if pcfg.Network {
					network = "enabled_by_profile"
				}
				profiles[pname] = codex.ProfileConfig{Claims: pcfg.Claims, Sandbox: "workspace-write", Approval: "never", Network: network}
			}
			run := codex.New(codex.Config{Command: rcfg.Command, DefaultProfile: rcfg.DefaultProfile, Profiles: profiles})
			wrapped, err := applyManifestPath(run, rcfg.ManifestPath)
			if err != nil {
				return nil, err
			}
			if err := reg.Register(wrapped); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unknown runner %q", name)
		}
	}
	return reg, nil
}

type manifestOverrideRunner struct {
	runner.Runner
	manifest runner.CapabilityManifest
}

func (r manifestOverrideRunner) Manifest() runner.CapabilityManifest {
	return r.manifest
}

func applyManifestPath(run runner.Runner, path string) (runner.Runner, error) {
	if path == "" {
		return run, nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read runner manifest %s: %w", path, err)
	}
	var manifest runner.CapabilityManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return nil, fmt.Errorf("parse runner manifest %s: %w", path, err)
	}
	if manifest.Runner == "" {
		manifest.Runner = run.Name()
	}
	return manifestOverrideRunner{Runner: run, manifest: manifest}, nil
}

func failClosedRestart(ctx context.Context, dataDir string, st *store.Store, auditWriter *audit.Writer) error {
	active, err := st.ListTasksByState(ctx, task.StateQueued, task.StateRunning)
	if err != nil {
		return err
	}
	for _, rec := range active {
		updated, err := st.UpdateTaskState(ctx, rec.ID, store.StateUpdate{State: task.StateFailed, TerminalErrorCode: task.ErrorWorkerDaemonRestarted})
		if err != nil {
			return err
		}
		warnCleanup(cleanupTaskSecrets(dataDir, updated.ID))
		event, err := auditWriter.Append(audit.Event{
			Type:           "task.failed",
			TaskID:         updated.ID,
			OriginSession:  updated.OriginSessionID,
			Chain:          updated.DelegationChain,
			Runner:         updated.Runner,
			Profile:        updated.ProfileEffective,
			PolicyDecision: "fail_closed_restart",
			Source:         "daemon_startup",
			ErrorCode:      string(task.ErrorWorkerDaemonRestarted),
			ErrorDetail:    "daemon restarted with queued or running task in store",
		})
		if err == nil {
			_ = st.RecordAuditEvent(ctx, event)
		}
	}
	return nil
}

func cleanupRetainedTaskArtifacts(ctx context.Context, cfg *config.Config, st *store.Store) error {
	cutoff := time.Now().UTC().Add(-task.DefaultIdempotencyDays * 24 * time.Hour)
	tasks, err := st.TerminalTasksCompletedBefore(ctx, cutoff, 1000)
	if err != nil {
		return err
	}
	var cleanupErr error
	for _, rec := range tasks {
		if err := cleanupTaskArtifacts(ctx, cfg.Node.DataDir, rec); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	if _, err = st.DeleteExpiredIdempotencyRecords(ctx, time.Now().UTC()); err != nil {
		cleanupErr = errors.Join(cleanupErr, err)
	}
	return cleanupErr
}

func cleanupLoop(ctx context.Context, cfg *config.Config, st *store.Store) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			warnCleanup(cleanupRetainedTaskArtifacts(ctx, cfg, st))
		}
	}
}

func warnCleanup(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "cmesh cleanup warning: %v\n", err)
	}
}

func cleanupTaskArtifacts(ctx context.Context, dataDir string, rec task.Record) error {
	var cleanupErr error
	var repo *gitx.Repository
	if rec.Result != nil && rec.Result.Branch != "" {
		opened, err := gitx.Open(rec.CWD)
		if err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		} else {
			repo = opened
		}
		if worktreePath := stringValue(rec.Result.RunnerExtra, "worktree_path"); worktreePath != "" {
			if repo != nil {
				if err := repo.RemoveWorktree(ctx, filepath.FromSlash(worktreePath)); err != nil {
					cleanupErr = errors.Join(cleanupErr, err)
				}
			} else if err := os.RemoveAll(filepath.FromSlash(worktreePath)); err != nil {
				cleanupErr = errors.Join(cleanupErr, err)
			}
		}
	}
	if err := cleanupTaskFiles(dataDir, rec.ID); err != nil {
		cleanupErr = errors.Join(cleanupErr, err)
	}
	if rec.Result != nil && rec.Result.Branch != "" && repo != nil {
		if err := repo.PruneWorktrees(ctx); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
		if err := repo.DeleteBranch(ctx, rec.Result.Branch); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	return cleanupErr
}

func cleanupTaskFiles(dataDir, taskID string) error {
	if err := cleanupTaskSecrets(dataDir, taskID); err != nil {
		return err
	}
	contextPath := filepath.Join(dataDir, "task-contexts", taskID+".json")
	for _, path := range []string{
		contextPath,
		filepath.Join(dataDir, "artifacts", taskID+".bundle"),
		filepath.Join(dataDir, "worktrees", taskID),
	} {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

func cleanupTaskSecrets(dataDir, taskID string) error {
	contextPath := filepath.Join(dataDir, "task-contexts", taskID+".json")
	if body, err := os.ReadFile(contextPath); err == nil {
		var raw struct {
			TaskTokenHash string `json:"task_token_hash"`
		}
		if json.Unmarshal(body, &raw) == nil && raw.TaskTokenHash != "" {
			if err := os.Remove(filepath.Join(dataDir, "task-tokens", raw.TaskTokenHash+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	if err := os.Remove(filepath.Join(dataDir, "task-mcp", taskID+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func stringValue(values map[string]any, key string) string {
	if len(values) == 0 {
		return ""
	}
	if value, ok := values[key].(string); ok {
		return value
	}
	return ""
}

func usage() {
	fmt.Println("usage: cmesh <up|doctor|status|log|connect|hello|version>")
}
