package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	osuser "os/user"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"cmesh/internal/audit"
	"cmesh/internal/config"
	"cmesh/internal/doctor"
	"cmesh/internal/gitx"
	tsid "cmesh/internal/identity/tailscale"
	"cmesh/internal/mcp/gateway"
	"cmesh/internal/mcp/setupinstall"
	"cmesh/internal/mcp/setupstdio"
	meshrpc "cmesh/internal/mesh/rpc"
	"cmesh/internal/mesh/schema"
	"cmesh/internal/onboard"
	"cmesh/internal/policy"
	"cmesh/internal/runner"
	"cmesh/internal/runner/claudecode"
	"cmesh/internal/runner/codex"
	"cmesh/internal/store"
	"cmesh/internal/task"
	"cmesh/internal/version"

	"golang.org/x/term"
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
	case "init":
		return cmdInit(args[1:])
	case "_service":
		return cmdServiceRun(args[1:])
	case "up":
		return cmdUp(args[1:])
	case "doctor":
		return cmdDoctor(args[1:])
	case "peers":
		return cmdPeers(args[1:])
	case "mcp":
		return cmdMCP(args[1:])
	case "test":
		return cmdTest(args[1:])
	case "connect":
		return cmdConnect(args[1:])
	case "status":
		return cmdStatus(args[1:])
	case "log":
		return cmdLog(args[1:])
	case "hello":
		return cmdHello(args[1:])
	case "version":
		return cmdVersion(args[1:])
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func cmdMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	stdio := fs.Bool("stdio", false, "run the setup MCP server over stdio")
	install := fs.Bool("install", false, "install the setup MCP server into a detected AI coding agent")
	agent := fs.String("agent", "", "agent to install into with --install: claude-code, codex, or all")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *install || (!*stdio && isTerminal(os.Stdin) && isTerminal(os.Stdout)) {
		return cmdMCPInstall(*agent)
	}
	return (&setupstdio.Server{}).Serve(context.Background())
}

func cmdMCPInstall(agentChoice string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	agents := setupinstall.DetectAgents()
	var found []setupinstall.Agent
	for _, agent := range agents {
		if agent.Found {
			found = append(found, agent)
		}
	}
	fmt.Println("cmesh MCP installer")
	fmt.Println()
	fmt.Println("Detected AI coding agents:")
	if len(found) == 0 {
		for _, agent := range agents {
			fmt.Printf("  - %s: %s\n", agent.Name, agent.Detail)
		}
		return errors.New("no supported AI coding agents found")
	}
	for i, agent := range found {
		fmt.Printf("[%d] %s  %s\n", i+1, agent.Name, agent.Detail)
	}
	allChoice := len(found) + 1
	if len(found) > 1 {
		fmt.Printf("[%d] all detected agents\n", allChoice)
	}
	selected, err := selectMCPInstallAgents(agentChoice, found, allChoice)
	if err != nil {
		return err
	}
	fmt.Println()
	for _, agent := range selected {
		result := setupinstall.Install(agent, exe)
		printCheck(agent.Name, result.OK, result.Detail)
		if !result.OK {
			err = errors.New("one or more MCP installs failed")
		}
	}
	if err != nil {
		return err
	}
	fmt.Println()
	fmt.Println("Done. Restart or reload the selected agent so it discovers cmesh.")
	return nil
}

func selectMCPInstallAgents(agentChoice string, found []setupinstall.Agent, allChoice int) ([]setupinstall.Agent, error) {
	agentChoice = strings.TrimSpace(agentChoice)
	if agentChoice != "" {
		if agentChoice == "all" {
			return found, nil
		}
		for _, agent := range found {
			if agent.ID == agentChoice {
				return []setupinstall.Agent{agent}, nil
			}
		}
		return nil, fmt.Errorf("agent %q is not detected; choose claude-code, codex, or all", agentChoice)
	}
	reader := bufio.NewReader(os.Stdin)
	defaultChoice := "1"
	if len(found) > 1 {
		defaultChoice = fmt.Sprint(allChoice)
	}
	answer, err := prompt(reader, fmt.Sprintf("Install cmesh MCP into which agent? [%s]: ", defaultChoice))
	if err != nil {
		return nil, err
	}
	if answer == "" {
		answer = defaultChoice
	}
	var choice int
	_, _ = fmt.Sscanf(answer, "%d", &choice)
	if choice == allChoice && len(found) > 1 {
		return found, nil
	}
	if choice >= 1 && choice <= len(found) {
		return []setupinstall.Agent{found[choice-1]}, nil
	}
	return nil, fmt.Errorf("invalid selection %q", answer)
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	configPath := fs.String("config", config.DefaultPath(), "path to cmesh config")
	role := fs.String("role", "", "machine role: origin, worker, or both")
	origin := fs.Bool("origin", false, "configure as an origin")
	worker := fs.Bool("worker", false, "configure as a worker")
	both := fs.Bool("both", false, "configure as both origin and worker")
	allowPeer := multiFlag{}
	allowCWD := multiFlag{}
	peer := multiFlag{}
	profile := multiFlag{}
	fs.Var(&allowPeer, "allow-peer", "peer node/name allowed to delegate here; repeatable")
	fs.Var(&allowCWD, "allow-cwd", "directory glob workers may use here; repeatable")
	fs.Var(&peer, "peer", "peer to configure for outbound delegation; repeatable")
	fs.Var(&profile, "profile", "profile to expose: readonly, dev, network, git_push; repeatable")
	runnerName := fs.String("runner", "auto", "runner to use: auto, claude-code, or codex")
	yes := fs.Bool("yes", false, "accept safe defaults without prompting")
	force := fs.Bool("force", false, "overwrite an existing config")
	connect := fs.Bool("connect", true, "register the local MCP gateway after writing config")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *origin {
		*role = "origin"
	}
	if *worker {
		*role = "worker"
	}
	if *both {
		*role = "both"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	machine := onboard.DetectMachine(ctx)
	runners := onboard.DetectRunners(ctx)
	peers, _ := onboard.TailnetPeers(ctx, config.DefaultMeshPort, true)
	printWelcome(machine, runners, peers)
	opts := onboard.InitOptions{
		Role:         *role,
		ConfigPath:   *configPath,
		Yes:          *yes,
		Connect:      *connect,
		AllowedPeers: []string(allowPeer),
		AllowedCWDs:  []string(allowCWD),
		PeerNames:    []string(peer),
		Runner:       *runnerName,
		Profiles:     []string(profile),
	}
	if !*yes {
		if err := promptInit(&opts, peers); err != nil {
			return err
		}
	}
	body, err := onboard.GenerateConfig(opts, machine, runners, peers)
	if err != nil {
		return err
	}
	if err := onboard.WriteConfig(*configPath, body, *force); err != nil {
		return err
	}
	if err := onboard.ValidateConfig(*configPath); err != nil {
		return err
	}
	fmt.Printf("\ncmesh is ready.\n\nConfig:\n  %s\n\nLocal MCP gateway:\n  http://127.0.0.1:%d/mcp\n\n", *configPath, config.DefaultMCPPort)
	if *connect {
		if err := cmdConnect([]string{"--config", *configPath}); err != nil {
			fmt.Fprintf(os.Stderr, "connect warning: %v\n", err)
		}
	}
	fmt.Println("Try this in your agent:")
	fmt.Println(`  "Use cmesh to ask a configured peer to summarize ~/code/myapp."`)
	return nil
}

func cmdUp(args []string) error {
	if len(args) > 0 && args[0] == "service" {
		return cmdService(args[1:])
	}
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	configPath := fs.String("config", config.DefaultPath(), "path to cmesh config")
	skipPreflight := fs.Bool("skip-preflight", false, "skip runner startup self-checks")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runDaemon(ctx, *configPath, !*skipPreflight)
}

func runDaemon(ctx context.Context, configPath string, preflight bool) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
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
	if preflight {
		checks := daemonPreflightChecks(ctx, cfg, registry)
		fmt.Println("cmesh preflight")
		fmt.Println()
		for _, check := range checks {
			printCheck(check.Name, check.OK, firstNonEmpty(check.Detail, check.Err))
		}
		fmt.Println()
		if !checksOK(checks) {
			return errors.New("cmesh preflight failed; fix the checks or use --skip-preflight to start anyway")
		}
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

func cmdServiceRun(args []string) error {
	fs := flag.NewFlagSet("_service", flag.ExitOnError)
	configPath := fs.String("config", config.DefaultPath(), "path to cmesh config")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return runServiceDaemon(*configPath)
}

func cmdDoctor(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "tailscale":
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
		case "runner":
			return cmdDoctorRunner(args[1:])
		case "service-context":
			return cmdDoctorServiceContext(args[1:])
		}
	}
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	configPath := fs.String("config", config.DefaultPath(), "path to cmesh config")
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	status := onboard.Detect(ctx, *configPath, false)
	cfg, cfgErr := config.Load(*configPath)
	var runnerChecks []doctor.Check
	if cfgErr == nil {
		reg, err := buildRunners(cfg)
		if err != nil {
			runnerChecks = append(runnerChecks, doctor.Check{Name: "runners", OK: false, Err: err.Error()})
		} else {
			for _, manifest := range reg.Manifests() {
				run, _ := reg.Get(manifest.Runner)
				err := run.Check("")
				check := doctor.Check{Name: "runner." + manifest.Runner, OK: err == nil}
				if err != nil {
					check.Err = err.Error()
				} else {
					check.Detail = "available"
				}
				runnerChecks = append(runnerChecks, check)
			}
		}
	}
	meshPort := config.DefaultMeshPort
	if cfg != nil {
		meshPort = cfg.Node.MeshPort
	}
	tailscaleReport := doctor.Tailscale(ctx, doctor.TailscaleOptions{MeshPort: meshPort})
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"status":    status,
			"tailscale": tailscaleReport.Checks,
			"runners":   runnerChecks,
		})
	}
	ok := true
	fmt.Println("cmesh doctor")
	fmt.Println()
	printCheck("config", cfgErr == nil, firstErrDetail("valid", cfgErr))
	if cfgErr != nil {
		ok = false
	}
	for _, check := range tailscaleReport.Checks {
		printCheck(check.Name, check.OK, firstNonEmpty(check.Detail, check.Err))
		if !check.OK {
			ok = false
		}
	}
	for _, runner := range status.Runners {
		printCheck("detect."+runner.Name, runner.Found, runner.Detail)
	}
	for _, check := range runnerChecks {
		printCheck(check.Name, check.OK, firstNonEmpty(check.Detail, check.Err))
		if !check.OK {
			ok = false
		}
	}
	if cfg != nil {
		printCheck("mcp", cfg.Node.MCPPort > 0, fmt.Sprintf("http://127.0.0.1:%d/mcp", cfg.Node.MCPPort))
	}
	if !ok {
		return errors.New("doctor found problems")
	}
	return nil
}

func cmdDoctorRunner(args []string) error {
	fs := flag.NewFlagSet("doctor runner", flag.ExitOnError)
	configPath := fs.String("config", config.DefaultPath(), "path to cmesh config")
	jsonOut := fs.Bool("json", false, "print JSON")
	skipSmoke := fs.Bool("skip-smoke", false, "skip invoking the runner smoke prompt")
	timeoutSeconds := fs.Int("timeout", 60, "runner smoke timeout in seconds")
	if err := parseInterspersedFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: cmesh doctor runner <runner> [--config path] [--skip-smoke]")
	}
	cfg, err := config.Load(*configPath)
	checks := []doctor.Check{{Name: "config", OK: err == nil, Detail: "valid"}}
	if err != nil {
		checks[0].Detail = ""
		checks[0].Err = err.Error()
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutSeconds+5)*time.Second)
		defer cancel()
		checks = append(checks, doctorRunnerChecks(ctx, cfg, fs.Arg(0), !*skipSmoke, time.Duration(*timeoutSeconds)*time.Second)...)
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"checks": checks})
	}
	fmt.Printf("cmesh doctor runner %s\n\n", fs.Arg(0))
	for _, check := range checks {
		printCheck(check.Name, check.OK, firstNonEmpty(check.Detail, check.Err))
	}
	if !checksOK(checks) {
		return errors.New("doctor runner found problems")
	}
	return nil
}

func daemonPreflightChecks(ctx context.Context, cfg *config.Config, reg *runner.Registry) []doctor.Check {
	var checks []doctor.Check
	checks = append(checks, doctor.Check{Name: "node id", OK: strings.TrimSpace(cfg.Node.ID) != "", Detail: cfg.Node.ID})
	if info, err := os.Stat(cfg.Node.DataDir); err != nil {
		if err := os.MkdirAll(cfg.Node.DataDir, 0o700); err != nil {
			checks = append(checks, doctor.Check{Name: "data directory", OK: false, Detail: cfg.Node.DataDir, Err: err.Error()})
		} else {
			checks = append(checks, doctor.Check{Name: "data directory", OK: true, Detail: cfg.Node.DataDir})
		}
	} else if !info.IsDir() {
		checks = append(checks, doctor.Check{Name: "data directory", OK: false, Detail: cfg.Node.DataDir, Err: "path is not a directory"})
	} else {
		checks = append(checks, doctor.Check{Name: "data directory", OK: true, Detail: cfg.Node.DataDir})
	}
	tailscaleReport := doctor.Tailscale(ctx, doctor.TailscaleOptions{MeshPort: cfg.Node.MeshPort})
	for _, check := range tailscaleReport.Checks {
		if check.Name == "localapi" || check.Name == "tailnet_ip" || check.Name == "whois_self" {
			checks = append(checks, check)
		}
	}
	for _, runnerName := range reg.Names() {
		rcfg := cfg.Runners[runnerName]
		if len(rcfg.Command) == 0 {
			checks = append(checks, doctor.Check{Name: "runner." + runnerName + ".command", OK: false, Err: "runner command is empty"})
			continue
		}
		exe := rcfg.Command[0]
		resolved, err := exec.LookPath(exe)
		if err != nil {
			checks = append(checks, doctor.Check{Name: "runner." + runnerName + ".executable", OK: false, Err: err.Error()})
		} else {
			checks = append(checks, doctor.Check{Name: "runner." + runnerName + ".executable", OK: true, Detail: resolved})
		}
		checks = append(checks, runnerProfileChecks(cfg, reg, runnerName)...)
		checks = append(checks, runnerSmokeCheck(ctx, reg, runnerName, 60*time.Second))
	}
	return checks
}

func doctorRunnerChecks(ctx context.Context, cfg *config.Config, runnerName string, runSmoke bool, timeout time.Duration) []doctor.Check {
	var checks []doctor.Check
	rcfg, ok := cfg.Runners[runnerName]
	checks = append(checks, doctor.Check{Name: "runner configured", OK: ok, Detail: runnerName})
	if !ok {
		checks[len(checks)-1].Err = "runner is not configured"
		checks[len(checks)-1].Detail = ""
		return checks
	}
	if len(rcfg.Command) == 0 {
		checks = append(checks, doctor.Check{Name: "runner executable exists", OK: false, Err: "runner command is empty"})
		return checks
	}
	exe := rcfg.Command[0]
	resolved, err := exec.LookPath(exe)
	if err != nil {
		checks = append(checks, doctor.Check{Name: "runner executable exists", OK: false, Err: err.Error()})
	} else {
		checks = append(checks, doctor.Check{Name: "runner executable exists", OK: true, Detail: resolved})
	}
	checks = append(checks, doctor.Check{
		Name:   "runner executable absolute",
		OK:     filepath.IsAbs(exe),
		Detail: exe,
	})
	if !filepath.IsAbs(exe) {
		checks[len(checks)-1].Err = "configure an absolute runner path for service use"
		checks[len(checks)-1].Detail = firstNonEmpty(resolved, exe)
	}
	if runtime.GOOS == "windows" {
		checks = append(checks, windowsServiceUserChecks()...)
	}
	reg, err := buildRunners(cfg)
	if err != nil {
		checks = append(checks, doctor.Check{Name: "runner auth works", OK: false, Err: err.Error()})
		return checks
	}
	checks = append(checks, runnerProfileChecks(cfg, reg, runnerName)...)
	if !runSmoke {
		checks = append(checks,
			doctor.Check{Name: "runner auth works", OK: true, Detail: "skipped"},
			doctor.Check{Name: "prompt via stdin works", OK: true, Detail: "skipped"},
			doctor.Check{Name: "smoke response verified", OK: true, Detail: "skipped"},
		)
		return checks
	}
	smoke := runnerSmokeCheck(ctx, reg, runnerName, timeout)
	checks = append(checks,
		doctor.Check{Name: "runner auth works", OK: smoke.OK, Detail: smoke.Detail, Err: smoke.Err},
		doctor.Check{Name: "prompt via stdin works", OK: smoke.OK, Detail: smoke.Detail, Err: smoke.Err},
		doctor.Check{Name: "smoke response verified", OK: smoke.OK, Detail: smoke.Detail, Err: smoke.Err},
	)
	return checks
}

func runnerProfileChecks(cfg *config.Config, reg *runner.Registry, runnerName string) []doctor.Check {
	run, ok := reg.Get(runnerName)
	if !ok {
		return []doctor.Check{{Name: "runner." + runnerName + ".registered", OK: false, Err: "runner not registered"}}
	}
	var checks []doctor.Check
	foundProfile := false
	for profileName, profile := range cfg.Profiles {
		if profile.Runner != runnerName {
			continue
		}
		foundProfile = true
		err := run.Check(profileName)
		check := doctor.Check{Name: "runner." + runnerName + ".profile." + profileName, OK: err == nil, Detail: "available"}
		if err != nil {
			check.Detail = ""
			check.Err = err.Error()
		}
		checks = append(checks, check)
	}
	if !foundProfile {
		err := run.Check("")
		check := doctor.Check{Name: "runner." + runnerName + ".profile.default", OK: err == nil, Detail: "available"}
		if err != nil {
			check.Detail = ""
			check.Err = err.Error()
		}
		checks = append(checks, check)
	}
	return checks
}

func runnerSmokeCheck(ctx context.Context, reg *runner.Registry, runnerName string, timeout time.Duration) doctor.Check {
	run, ok := reg.Get(runnerName)
	if !ok {
		return doctor.Check{Name: "runner." + runnerName + ".smoke", OK: false, Err: "runner not registered"}
	}
	result, err := run.Run(runner.RunRequest{
		Context:     ctx,
		TaskID:      "doctor-runner-smoke",
		Prompt:      "Reply with exactly: " + smokeExpectedResponse,
		CWD:         ".",
		Timeout:     timeout,
		OutputLimit: runner.DefaultOutputLimit,
	})
	if err != nil {
		return doctor.Check{Name: "runner." + runnerName + ".smoke", OK: false, Err: err.Error()}
	}
	output := commandOutputText(result.CommandOutput.Stdout, result.CommandOutput.Stderr)
	detail := oneLine(firstNonEmpty(output, result.WorkerSummaryUntrusted))
	okSmoke := result.CommandOutput.Command != "" && result.CommandOutput.ExitCode == 0 && rawOutputContainsSmoke(result.CommandOutput.Stdout, result.CommandOutput.Stderr)
	if okSmoke {
		detail = smokeExpectedResponse
	}
	check := doctor.Check{Name: "runner." + runnerName + ".smoke", OK: okSmoke, Detail: detail}
	if !okSmoke {
		check.Detail = ""
		check.Err = fmt.Sprintf("expected raw command artifact with exit=0 and %q, got exit=%d output=%q", smokeExpectedResponse, result.CommandOutput.ExitCode, output)
	}
	return check
}

func cmdDoctorServiceContext(args []string) error {
	fs := flag.NewFlagSet("doctor service-context", flag.ExitOnError)
	configPath := fs.String("config", config.DefaultPath(), "path to cmesh config")
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	checks := serviceContextChecks(ctx, *configPath)
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"checks": checks})
	}
	fmt.Println("cmesh doctor service-context")
	fmt.Println()
	for _, check := range checks {
		printCheck(check.Name, check.OK, firstNonEmpty(check.Detail, check.Err))
	}
	if !checksOK(checks) {
		return errors.New("doctor service-context found problems")
	}
	return nil
}

func serviceContextChecks(ctx context.Context, configPath string) []doctor.Check {
	var checks []doctor.Check
	current, err := osuser.Current()
	if err != nil {
		checks = append(checks, doctor.Check{Name: "user identity", OK: false, Err: err.Error()})
	} else {
		checks = append(checks, doctor.Check{Name: "user identity", OK: true, Detail: firstNonEmpty(current.Username, current.Uid)})
	}
	checks = append(checks, windowsServiceUserChecks()...)
	pathValue := os.Getenv("PATH")
	checks = append(checks, doctor.Check{Name: "PATH", OK: pathValue != "", Detail: fmt.Sprintf("%d bytes", len(pathValue))})
	if pathValue == "" {
		checks[len(checks)-1].Err = "PATH is empty"
	}
	home := homeDir()
	if info, err := os.Stat(home); err != nil || !info.IsDir() {
		checks = append(checks, doctor.Check{Name: "home directory", OK: false, Detail: home, Err: firstErrDetail("", err)})
	} else {
		checks = append(checks, doctor.Check{Name: "home directory", OK: true, Detail: home})
	}
	claudeConfig := filepath.Join(home, ".claude")
	if info, err := os.Stat(claudeConfig); err != nil || !info.IsDir() {
		checks = append(checks, doctor.Check{Name: "Claude config location", OK: false, Detail: claudeConfig, Err: firstErrDetail("not found", err)})
	} else {
		checks = append(checks, doctor.Check{Name: "Claude config location", OK: true, Detail: claudeConfig})
	}
	tailscaleReport := doctor.Tailscale(ctx, doctor.TailscaleOptions{MeshPort: config.DefaultMeshPort})
	for _, check := range tailscaleReport.Checks {
		if check.Name == "localapi" {
			checks = append(checks, doctor.Check{Name: "Tailscale LocalAPI access", OK: check.OK, Detail: firstNonEmpty(check.Detail, check.Err), Err: check.Err})
			break
		}
	}
	if cwd, err := os.Getwd(); err != nil {
		checks = append(checks, doctor.Check{Name: "cwd access", OK: false, Err: err.Error()})
	} else if _, err := os.Stat(cwd); err != nil {
		checks = append(checks, doctor.Check{Name: "cwd access", OK: false, Detail: cwd, Err: err.Error()})
	} else {
		checks = append(checks, doctor.Check{Name: "cwd access", OK: true, Detail: cwd})
	}
	if gitPath, err := exec.LookPath("git"); err != nil {
		checks = append(checks, doctor.Check{Name: "git access", OK: false, Err: err.Error()})
	} else {
		checks = append(checks, doctor.Check{Name: "git access", OK: true, Detail: gitPath})
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		checks = append(checks, doctor.Check{Name: "config", OK: false, Err: err.Error()})
		return checks
	}
	checks = append(checks, doctor.Check{Name: "config", OK: true, Detail: configPath})
	reg, err := buildRunners(cfg)
	if err != nil {
		checks = append(checks, doctor.Check{Name: "spawn runner", OK: false, Err: err.Error()})
		return checks
	}
	for _, manifest := range reg.Manifests() {
		run, _ := reg.Get(manifest.Runner)
		err := run.Check("")
		check := doctor.Check{Name: "spawn runner." + manifest.Runner, OK: err == nil, Detail: "available"}
		if err != nil {
			check.Detail = ""
			check.Err = err.Error()
		}
		checks = append(checks, check)
	}
	return checks
}

func windowsServiceUserChecks() []doctor.Check {
	if runtime.GOOS != "windows" {
		return []doctor.Check{{Name: "service user", OK: true, Detail: "not windows"}}
	}
	serviceUser, err := windowsServiceStartName()
	if err != nil {
		if isWindowsServiceNotInstalledError(err) {
			return []doctor.Check{{Name: "service user", OK: true, Detail: "not installed"}}
		}
		return []doctor.Check{{Name: "service user", OK: false, Err: err.Error()}}
	}
	current, currentErr := osuser.Current()
	if currentErr != nil {
		return []doctor.Check{
			{Name: "service user", OK: true, Detail: serviceUser},
			{Name: "interactive user mismatch", OK: false, Err: currentErr.Error()},
		}
	}
	mismatch := !sameWindowsUser(serviceUser, current.Username)
	return []doctor.Check{
		{Name: "service user", OK: true, Detail: serviceUser},
		{Name: "interactive user mismatch", OK: !mismatch, Detail: yesNo(mismatch)},
	}
}

func windowsServiceStartName() (string, error) {
	out, err := exec.Command("sc.exe", "qc", "cmesh").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("query Windows service cmesh: %s", strings.TrimSpace(string(out)))
	}
	return parseWindowsServiceStartName(string(out))
}

func isWindowsServiceNotInstalledError(err error) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	return strings.Contains(text, "FAILED 1060") || strings.Contains(text, "does not exist as an installed service")
}

func parseWindowsServiceStartName(out string) (string, error) {
	for _, line := range strings.Split(out, "\n") {
		if before, after, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(before) == "SERVICE_START_NAME" {
			value := strings.TrimSpace(after)
			if value == "" {
				return "", errors.New("SERVICE_START_NAME is empty")
			}
			return value, nil
		}
	}
	return "", errors.New("SERVICE_START_NAME not found")
}

func sameWindowsUser(a, b string) bool {
	a = normalizeWindowsUserName(a)
	b = normalizeWindowsUserName(b)
	return a != "" && b != "" && a == b
}

func normalizeWindowsUserName(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimPrefix(value, `.\`)
	if i := strings.LastIndex(value, `\`); i >= 0 {
		value = value[i+1:]
	}
	return value
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func checksOK(checks []doctor.Check) bool {
	for _, check := range checks {
		if !check.OK {
			return false
		}
	}
	return true
}

func cmdService(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: cmesh up service <install|start|stop|restart|status|logs>")
	}
	switch args[0] {
	case "install":
		fs := flag.NewFlagSet("up service install", flag.ExitOnError)
		configPath := fs.String("config", config.DefaultPath(), "path to cmesh config")
		user := fs.String("user", "", `Windows service account, such as ".\scott" or "DOMAIN\user"`)
		password := fs.String("password", "", "Windows service account password; prefer --prompt-password or --password-env")
		passwordEnv := fs.String("password-env", "", "environment variable containing the Windows service account password")
		promptPassword := fs.Bool("prompt-password", false, "prompt for the Windows service account password")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return serviceInstall(serviceInstallOptions{
			ConfigPath:     *configPath,
			User:           *user,
			Password:       *password,
			PasswordEnv:    *passwordEnv,
			PromptPassword: *promptPassword,
		})
	case "start":
		return serviceStart()
	case "stop":
		return serviceStop()
	case "restart":
		return serviceRestart()
	case "status":
		return serviceStatus()
	case "logs":
		return serviceLogs()
	default:
		return fmt.Errorf("usage: cmesh up service <install|start|stop|restart|status|logs>")
	}
}

type serviceInstallOptions struct {
	ConfigPath     string
	User           string
	Password       string
	PasswordEnv    string
	PromptPassword bool
}

func serviceInstall(opts serviceInstallOptions) error {
	configPath := firstNonEmpty(opts.ConfigPath, config.DefaultPath())
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if runtime.GOOS != "windows" && (opts.User != "" || opts.Password != "" || opts.PasswordEnv != "" || opts.PromptPassword) {
		return fmt.Errorf("Windows service account options are only supported on Windows")
	}
	switch runtime.GOOS {
	case "linux":
		unitDir := filepath.Join(userConfigDir(), "systemd", "user")
		if err := os.MkdirAll(unitDir, 0o755); err != nil {
			return err
		}
		unitPath := filepath.Join(unitDir, "cmesh.service")
		body := fmt.Sprintf("[Unit]\nDescription=cmesh agent mesh daemon\nAfter=network-online.target\n\n[Service]\nExecStart=%s up --config %s\nRestart=on-failure\nRestartSec=3\n\n[Install]\nWantedBy=default.target\n", exe, configPath)
		if err := os.WriteFile(unitPath, []byte(body), 0o644); err != nil {
			return err
		}
		_ = runServiceCommand("systemctl", "--user", "daemon-reload")
		if err := runServiceCommand("systemctl", "--user", "enable", "cmesh.service"); err != nil {
			return err
		}
		fmt.Printf("Installed user systemd service: %s\n", unitPath)
	case "darwin":
		launchDir := filepath.Join(homeDir(), "Library", "LaunchAgents")
		if err := os.MkdirAll(launchDir, 0o755); err != nil {
			return err
		}
		plistPath := launchdPlistPath()
		body := fmt.Sprintf("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\"><dict><key>Label</key><string>dev.cmesh</string><key>ProgramArguments</key><array><string>%s</string><string>up</string><string>--config</string><string>%s</string></array><key>RunAtLoad</key><true/><key>KeepAlive</key><true/></dict></plist>\n", exe, configPath)
		if err := os.WriteFile(plistPath, []byte(body), 0o644); err != nil {
			return err
		}
		fmt.Printf("Installed launchd agent: %s\n", plistPath)
	case "windows":
		password, err := resolveWindowsServicePassword(opts)
		if err != nil {
			return err
		}
		cmdLine := serviceCommandLine(exe, configPath)
		action := "create"
		if windowsServiceExists() {
			action = "config"
		}
		args := windowsServiceSCArgs(action, cmdLine, opts.User, password)
		if err := runServiceCommand("sc.exe", args...); err != nil {
			return err
		}
		if opts.User == "" {
			fmt.Println("Installed Windows service: cmesh")
			fmt.Println("Warning: service will run as LocalSystem. Runner auth from your interactive user session will not be available.")
			fmt.Println(`For Claude Code/Codex auth, reinstall with: cmesh up service install --user ".\YOURUSER" --prompt-password`)
		} else if action == "config" {
			fmt.Printf("Updated Windows service cmesh to run as %s\n", opts.User)
		} else {
			fmt.Printf("Installed Windows service cmesh to run as %s\n", opts.User)
		}
	default:
		return fmt.Errorf("service install is not supported on %s", runtime.GOOS)
	}
	return nil
}

func serviceCommandLine(exe, configPath string) string {
	return fmt.Sprintf(`"%s" _service --config "%s"`, exe, configPath)
}

func resolveWindowsServicePassword(opts serviceInstallOptions) (string, error) {
	if opts.User == "" {
		if opts.Password != "" || opts.PasswordEnv != "" || opts.PromptPassword {
			return "", fmt.Errorf("Windows service password options require --user")
		}
		return "", nil
	}
	methods := 0
	if opts.Password != "" {
		methods++
	}
	if opts.PasswordEnv != "" {
		methods++
	}
	if opts.PromptPassword {
		methods++
	}
	if methods == 0 {
		return "", fmt.Errorf("Windows service --user requires --prompt-password, --password-env, or --password")
	}
	if methods > 1 {
		return "", fmt.Errorf("use only one of --prompt-password, --password-env, or --password")
	}
	if opts.Password != "" {
		return opts.Password, nil
	}
	if opts.PasswordEnv != "" {
		password := os.Getenv(opts.PasswordEnv)
		if password == "" {
			return "", fmt.Errorf("environment variable %s is empty", opts.PasswordEnv)
		}
		return password, nil
	}
	fmt.Fprintf(os.Stderr, "Password for Windows service account %s: ", opts.User)
	passwordBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	password := string(passwordBytes)
	if password == "" {
		return "", fmt.Errorf("password is empty")
	}
	return password, nil
}

func windowsServiceExists() bool {
	return exec.Command("sc.exe", "query", "cmesh").Run() == nil
}

func windowsServiceSCArgs(action, cmdLine, user, password string) []string {
	args := []string{action, "cmesh", "binPath=", cmdLine, "start=", "auto"}
	if user != "" {
		args = append(args, "obj=", user, "password=", password)
	}
	return args
}

func serviceStart() error {
	switch runtime.GOOS {
	case "linux":
		return runServiceCommand("systemctl", "--user", "start", "cmesh.service")
	case "darwin":
		domain := launchdDomain()
		target := domain + "/dev.cmesh"
		if err := exec.Command("launchctl", "print", target).Run(); err != nil {
			if err := runServiceCommand("launchctl", "bootstrap", domain, launchdPlistPath()); err != nil {
				return err
			}
		}
		return runServiceCommand("launchctl", "kickstart", "-k", target)
	case "windows":
		return runServiceCommand("sc.exe", "start", "cmesh")
	default:
		return fmt.Errorf("service start is not supported on %s", runtime.GOOS)
	}
}

func serviceStop() error {
	switch runtime.GOOS {
	case "linux":
		return runServiceCommand("systemctl", "--user", "stop", "cmesh.service")
	case "darwin":
		return runServiceCommand("launchctl", "bootout", launchdDomain()+"/dev.cmesh")
	case "windows":
		return runServiceCommand("sc.exe", "stop", "cmesh")
	default:
		return fmt.Errorf("service stop is not supported on %s", runtime.GOOS)
	}
}

func serviceRestart() error {
	switch runtime.GOOS {
	case "linux":
		return runServiceCommand("systemctl", "--user", "restart", "cmesh.service")
	case "darwin":
		return serviceStart()
	case "windows":
		if err := serviceStop(); err != nil {
			return err
		}
		time.Sleep(5 * time.Second)
		return serviceStart()
	default:
		return fmt.Errorf("service restart is not supported on %s", runtime.GOOS)
	}
}

func serviceStatus() error {
	switch runtime.GOOS {
	case "linux":
		return runServiceCommand("systemctl", "--user", "status", "cmesh.service", "--no-pager")
	case "darwin":
		return runServiceCommand("launchctl", "print", "gui/"+fmt.Sprint(os.Getuid())+"/dev.cmesh")
	case "windows":
		return runServiceCommand("sc.exe", "query", "cmesh")
	default:
		return fmt.Errorf("service status is not supported on %s", runtime.GOOS)
	}
}

func serviceLogs() error {
	switch runtime.GOOS {
	case "linux":
		return runServiceCommand("journalctl", "--user", "-u", "cmesh.service", "-n", "100", "--no-pager")
	case "darwin":
		return runServiceCommand("log", "show", "--predicate", `process == "cmesh"`, "--last", "1h")
	case "windows":
		return runServiceCommand("powershell", "-NoProfile", "-Command", `Get-EventLog -LogName Application -Source cmesh -Newest 100`)
	default:
		return fmt.Errorf("service logs are not supported on %s", runtime.GOOS)
	}
}

func launchdDomain() string {
	return "gui/" + fmt.Sprint(os.Getuid())
}

func launchdPlistPath() string {
	return filepath.Join(homeDir(), "Library", "LaunchAgents", "dev.cmesh.plist")
}

func runServiceCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}

func userConfigDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return filepath.Join(homeDir(), ".config")
	}
	return dir
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
		cmd := exec.Command(claude, setupinstall.ClaudeHTTPAddArgs(setupinstall.ServerName, gatewayURL)...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("register Claude Code MCP server: %w", err)
		}
		fmt.Println("Claude Code MCP registration updated.")
	} else {
		fmt.Println("Claude Code not found on PATH; register manually with:")
		fmt.Printf("  claude %s\n", strings.Join(setupinstall.ClaudeHTTPAddArgs(setupinstall.ServerName, gatewayURL), " "))
	}
	fmt.Println("Codex MCP registration varies by installed CLI version; add a local HTTP MCP server named cmesh at:")
	fmt.Printf("  %s\n", gatewayURL)
	return nil
}

func cmdBootstrap(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: cmesh peers bootstrap <ssh|tailscale> <target>")
	}
	mode := args[0]
	target := args[1]
	cfg, _ := config.Load(config.DefaultPath())
	origin := "this-machine"
	if cfg != nil {
		origin = firstNonEmpty(cfg.Node.Name, cfg.Node.ID, origin)
	}
	remote := fmt.Sprintf("curl -fsSL https://get.cmesh.dev | sh && cmesh init --worker --allow-peer %s --yes && cmesh up service install && cmesh up service start", shellQuote(origin))
	var cmd *exec.Cmd
	switch mode {
	case "ssh":
		cmd = exec.Command("ssh", target, remote)
	case "tailscale":
		cmd = exec.Command("tailscale", "ssh", target, remote)
	default:
		return fmt.Errorf("usage: cmesh peers bootstrap <ssh|tailscale> <target>")
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func cmdPeers(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "test":
			return cmdTest(args[1:])
		case "bootstrap":
			return cmdBootstrap(args[1:])
		}
	}
	fs := flag.NewFlagSet("peers", flag.ExitOnError)
	configPath := fs.String("config", config.DefaultPath(), "path to cmesh config")
	discover := fs.Bool("discover", true, "also show tailnet peer candidates")
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, cfgErr := config.Load(*configPath)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var discovered []onboard.PeerCandidate
	if *discover {
		discovered, _ = onboard.TailnetPeers(ctx, config.DefaultMeshPort, true)
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"configured": configuredPeers(cfg), "discovered": discovered})
	}
	if cfgErr == nil && len(cfg.Peers) > 0 {
		fmt.Println("Configured peers:")
		for name, peer := range cfg.Peers {
			fmt.Printf("  - %s  %s:%d\n", firstNonEmpty(peer.Name, name), peer.Address, firstNonZero(peer.Port, config.DefaultMeshPort))
		}
	} else if cfgErr != nil {
		fmt.Printf("Configured peers: unavailable (%v)\n", cfgErr)
	} else {
		fmt.Println("Configured peers: none")
	}
	if *discover {
		fmt.Println()
		fmt.Println("Discovered tailnet peers:")
		if len(discovered) == 0 {
			fmt.Println("  none found")
		}
		for _, peer := range discovered {
			capable := ""
			if peer.CmeshCapable {
				capable = " cmesh"
			}
			fmt.Printf("  - %s  %s%s\n", peer.Name, firstNonEmpty(peer.Address, "no tailnet ip"), capable)
		}
	}
	return nil
}

func cmdTest(args []string) error {
	fs := flag.NewFlagSet("test", flag.ExitOnError)
	configPath := fs.String("config", config.DefaultPath(), "path to cmesh config")
	cwd := fs.String("cwd", "~", "worker cwd for the smoke task")
	profile := fs.String("profile", "readonly", "profile to request")
	wait := fs.Bool("wait", true, "wait for the smoke task to finish")
	timeoutSeconds := fs.Int("timeout", 60, "smoke task timeout in seconds")
	bidirectional := fs.Bool("bidirectional", false, "also ask the peer daemon to run a direct smoke task back to this machine")
	reverseCWD := fs.String("reverse-cwd", "", "cwd on this machine for the peer's reverse smoke task; defaults to the current directory")
	if err := parseInterspersedFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: cmesh test <peer> [--cwd ~/code/myapp] [--bidirectional]")
	}
	if *bidirectional && !*wait {
		return fmt.Errorf("--bidirectional requires --wait=true")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	peerName := fs.Arg(0)
	peer, ok := cfg.Peers[peerName]
	if !ok {
		return fmt.Errorf("unknown peer %q", peerName)
	}
	port := firstNonZero(peer.Port, config.DefaultMeshPort)
	client := meshrpc.NewClient("http://" + net.JoinHostPort(peer.Address, fmt.Sprintf("%d", port)))
	ctxSeconds := *timeoutSeconds + 15
	if *bidirectional {
		ctxSeconds = *timeoutSeconds*2 + 30
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(ctxSeconds)*time.Second)
	defer cancel()
	st, err := store.OpenContext(ctx, cfg.Store.Path)
	if err != nil {
		return err
	}
	defer st.Close()
	auditWriter := audit.NewWriter(cfg.Audit.Path, cfg.Node.ID)
	fmt.Println("cmesh test")
	fmt.Println()
	hello, err := client.Hello(ctx)
	if err != nil {
		printCheck("peer reachable", false, err.Error())
		return err
	}
	printCheck("peer reachable", true, firstNonEmpty(hello.NodeName, hello.NodeID))
	chain := []string{cfg.Node.ID}
	decision := policy.New(cfg).CheckOutbound(policy.TaskIntent{
		PeerName:     firstNonEmpty(peer.Name, peerName),
		PeerNodeID:   peer.NodeID,
		CWD:          *cwd,
		Profile:      *profile,
		Chain:        chain,
		OriginNodeID: cfg.Node.ID,
	})
	if !decision.Allowed {
		printCheck("policy allowed", false, decision.Detail)
		return fmt.Errorf("%s: %s", decision.Code, decision.Detail)
	}
	printCheck("policy allowed", true, decision.EffectiveProfile)
	resp, err := client.CreateTask(ctx, schemaSmokeRequest(cfg, peerName, *cwd, *profile, *wait, *timeoutSeconds))
	if err != nil {
		printCheck("inbound policy / runner", false, err.Error())
		return err
	}
	event, err := auditWriter.Append(audit.Event{
		Type:           "task.dispatch.requested",
		TaskID:         resp.TaskID,
		PeerIdentity:   firstNonEmpty(peer.NodeID, peer.Name, peerName),
		OriginSession:  "cmesh-cli-smoke",
		Chain:          chain,
		Profile:        decision.EffectiveProfile,
		PolicyDecision: "allowed",
		Source:         "cmesh_cli",
	})
	if err != nil {
		printCheck("audit event written", false, err.Error())
		return err
	}
	if err := st.RecordAuditEvent(ctx, event); err != nil {
		printCheck("audit event written", false, err.Error())
		return err
	}
	auditEventWritten, err := st.HasAuditEvent(ctx, resp.TaskID, "task.dispatch.requested")
	if err != nil {
		printCheck("audit event written", false, err.Error())
		return err
	}
	printCheck("audit event written", auditEventWritten, "task.dispatch.requested")
	if !auditEventWritten {
		return fmt.Errorf("audit event was not recorded for %s", resp.TaskID)
	}
	printCheck("inbound policy allowed", true, string(resp.State))
	printCheck("task id", resp.TaskID != "", resp.TaskID)
	if *wait {
		if resp.State.Terminal() {
			if resp.State != task.StateDone {
				printCheck("task completed", false, string(resp.State))
				return fmt.Errorf("smoke task finished with state %s", resp.State)
			}
			printCheck("task completed", true, string(resp.State))
			result, err := client.TaskResult(ctx, resp.TaskID)
			if err != nil {
				printCheck("smoke response verified", false, err.Error())
				return err
			}
			resultEnvelopeParsed := result.Result != nil && task.State(result.Result.Status) == task.StateDone
			printCheck("result envelope parsed", resultEnvelopeParsed, string(task.StateDone))
			if !resultEnvelopeParsed {
				return fmt.Errorf("smoke task result envelope is not done")
			}
			runnerInvoked := result.Result.CommandOutput.Command != ""
			printCheck("runner invoked", runnerInvoked, result.Result.CommandOutput.Command)
			if !runnerInvoked {
				return fmt.Errorf("smoke task did not return a command_output command")
			}
			promptReceived := smokeResultMatches(result.Result)
			if !promptReceived {
				detail := "missing expected smoke response"
				if result.Result != nil && strings.TrimSpace(result.Result.WorkerSummaryUntrusted) != "" {
					detail = oneLine(result.Result.WorkerSummaryUntrusted)
				}
				printCheck("prompt received", false, detail)
				return fmt.Errorf("smoke task did not return %q", smokeExpectedResponse)
			}
			printCheck("prompt received", true, smokeExpectedResponse)
			printCheck("smoke response verified", true, smokeExpectedResponse)
		} else {
			printCheck("task queued", true, resp.TaskID)
		}
	} else {
		printCheck("task queued", true, resp.TaskID)
	}
	if *bidirectional {
		cwdBack := strings.TrimSpace(*reverseCWD)
		if cwdBack == "" {
			cwdBack, err = os.Getwd()
			if err != nil {
				return err
			}
		}
		fmt.Println()
		fmt.Println("reverse cmesh test")
		fmt.Println()
		reverse, err := client.PeerSmoke(ctx, schema.PeerSmokeRequest{CWD: cwdBack, Profile: *profile, TimeoutSeconds: *timeoutSeconds})
		if err != nil {
			printCheck("reverse smoke requested", false, err.Error())
			return err
		}
		if err := printPeerSmokeResponse(reverse); err != nil {
			return err
		}
	}
	return nil
}

const smokeExpectedResponse = "cmesh smoke ok"

func smokeResultMatches(result *task.ResultEnvelope) bool {
	if result == nil {
		return false
	}
	if result.CommandOutput.Command == "" {
		return false
	}
	return result.CommandOutput.ExitCode == 0 && rawOutputContainsSmoke(result.CommandOutput.Stdout, result.CommandOutput.Stderr)
}

func printPeerSmokeResponse(resp schema.PeerSmokeResponse) error {
	checks := resp.Checks
	if checks == nil {
		checks = map[string]bool{}
	}
	printCheck("reverse peer reachable", checks["peer_reachable"], firstNonEmpty(resp.NodeName, resp.NodeID, resp.Peer))
	printCheck("reverse policy allowed", checks["policy_allowed"], "done")
	printCheck("reverse task id", resp.TaskID != "", resp.TaskID)
	printCheck("reverse task completed", resp.State == task.StateDone, string(resp.State))
	resultParsed := checks["result_envelope_parsed"]
	printCheck("reverse result envelope parsed", resultParsed, string(task.StateDone))
	runnerCommand := ""
	if resp.Result != nil {
		runnerCommand = resp.Result.CommandOutput.Command
	}
	printCheck("reverse runner invoked", checks["runner_invoked"], runnerCommand)
	verified := checks["prompt_received"] && checks["smoke_response_verified"]
	detail := smokeExpectedResponse
	if !verified && resp.Error != nil {
		detail = resp.Error.Detail
	}
	printCheck("reverse prompt received", checks["prompt_received"], detail)
	printCheck("reverse smoke response verified", checks["smoke_response_verified"], detail)
	if resp.Error != nil {
		return fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Detail)
	}
	if !verified {
		return fmt.Errorf("reverse smoke task did not return %q", smokeExpectedResponse)
	}
	return nil
}

func rawOutputContainsSmoke(stdout, stderr string) bool {
	for _, value := range []string{stdout, stderr, commandOutputText(stdout, stderr)} {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if strings.Contains(value, smokeExpectedResponse) {
			return true
		}
		var raw map[string]any
		if json.Unmarshal([]byte(value), &raw) == nil {
			if strings.Contains(jsonStringField(raw, "result", "summary", "output", "message", "content", "final_answer"), smokeExpectedResponse) {
				return true
			}
		}
	}
	return false
}

func commandOutputText(stdout, stderr string) string {
	return strings.TrimSpace(strings.TrimSpace(stdout) + "\n" + strings.TrimSpace(stderr))
}

func jsonStringField(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			switch v := value.(type) {
			case string:
				return strings.TrimSpace(v)
			case []any:
				parts := make([]string, 0, len(v))
				for _, item := range v {
					parts = append(parts, fmt.Sprint(item))
				}
				return strings.TrimSpace(strings.Join(parts, "\n"))
			}
		}
	}
	return ""
}

func oneLine(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 96 {
		return value[:96] + "..."
	}
	return value
}

type multiFlag []string

func (m *multiFlag) String() string {
	return strings.Join(*m, ",")
}

func (m *multiFlag) Set(value string) error {
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			*m = append(*m, part)
		}
	}
	return nil
}

func parseInterspersedFlags(fs *flag.FlagSet, args []string) error {
	var flags []string
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		flags = append(flags, arg)
		if flagTakesValue(fs, arg) && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return fs.Parse(append(flags, positional...))
}

func flagTakesValue(fs *flag.FlagSet, arg string) bool {
	name := strings.TrimLeft(arg, "-")
	if name == "" {
		return false
	}
	if before, _, ok := strings.Cut(name, "="); ok {
		name = before
	}
	f := fs.Lookup(name)
	if f == nil || strings.Contains(arg, "=") {
		return false
	}
	type boolFlag interface {
		IsBoolFlag() bool
	}
	if bf, ok := f.Value.(boolFlag); ok && bf.IsBoolFlag() {
		return false
	}
	return true
}

func printWelcome(machine onboard.Machine, runners []onboard.RunnerStatus, peers []onboard.PeerCandidate) {
	fmt.Println("Welcome to cmesh.")
	fmt.Println()
	fmt.Println("This machine:")
	fmt.Printf("  Name: %s\n", machine.Name)
	if machine.TailscaleOK {
		fmt.Printf("  Tailscale: connected as %s\n", firstNonEmpty(machine.DNSName, machine.NodeID, machine.Name))
	} else {
		fmt.Printf("  Tailscale: not ready (%s)\n", firstNonEmpty(machine.Detail, "not connected"))
	}
	fmt.Printf("  Tailnet IP: %s\n", firstNonEmpty(strings.Join(machine.TailnetIPs, ", "), "not found"))
	for _, runner := range runners {
		state := "not found"
		if runner.Found {
			state = "found"
		}
		fmt.Printf("  %s: %s\n", displayRunner(runner.Name), state)
	}
	if len(peers) > 0 {
		fmt.Println()
		fmt.Println("Found cmesh-capable peers:")
		found := false
		for _, peer := range peers {
			if peer.CmeshCapable {
				fmt.Printf("  - %s\n", peer.Name)
				found = true
			}
		}
		if !found {
			fmt.Println("  none yet")
		}
	}
}

func promptInit(opts *onboard.InitOptions, peers []onboard.PeerCandidate) error {
	reader := bufio.NewReader(os.Stdin)
	if opts.Role == "" {
		fmt.Println()
		fmt.Println("What should this machine do?")
		fmt.Println("[1] Origin only: my local agents can delegate from here")
		fmt.Println("[2] Worker only: other machines can delegate work here")
		fmt.Println("[3] Both: delegate from here and accept work here")
		answer, err := prompt(reader, "Choose 1-3 [3]: ")
		if err != nil {
			return err
		}
		switch strings.TrimSpace(answer) {
		case "1":
			opts.Role = "origin"
		case "2":
			opts.Role = "worker"
		default:
			opts.Role = "both"
		}
	}
	if len(opts.AllowedCWDs) == 0 && opts.Role != "origin" {
		suggested := onboard.SuggestedCWDs()
		fmt.Println()
		fmt.Println("Which directories may workers use on this machine?")
		for i, cwd := range suggested {
			fmt.Printf("[%d] %s\n", i+1, cwd)
		}
		fmt.Printf("[%d] choose directories\n", len(suggested)+1)
		answer, err := prompt(reader, "Choose [1]: ")
		if err != nil {
			return err
		}
		choice := strings.TrimSpace(answer)
		if choice == fmt.Sprintf("%d", len(suggested)+1) {
			value, err := prompt(reader, "Directory globs, comma-separated: ")
			if err != nil {
				return err
			}
			opts.AllowedCWDs = splitCSV(value)
		} else {
			index := 0
			if choice != "" {
				_, _ = fmt.Sscanf(choice, "%d", &index)
				index--
			}
			if index < 0 || index >= len(suggested) {
				index = 0
			}
			opts.AllowedCWDs = []string{suggested[index]}
		}
	}
	if len(opts.PeerNames) == 0 && opts.Role != "worker" {
		fmt.Println()
		fmt.Println("Which peers may this machine delegate to?")
		capable := cmeshCapablePeers(peers)
		if len(capable) == 0 {
			value, err := prompt(reader, "Peer names, comma-separated (empty for later): ")
			if err != nil {
				return err
			}
			opts.PeerNames = splitCSV(value)
		} else {
			for i, peer := range capable {
				fmt.Printf("[%d] %s\n", i+1, peer.Name)
			}
			fmt.Printf("[%d] all cmesh-capable peers\n", len(capable)+1)
			answer, err := prompt(reader, fmt.Sprintf("Choose [%d]: ", len(capable)+1))
			if err != nil {
				return err
			}
			answer = strings.TrimSpace(answer)
			if answer == "" || answer == fmt.Sprintf("%d", len(capable)+1) {
				for _, peer := range capable {
					opts.PeerNames = append(opts.PeerNames, peer.Name)
				}
			} else {
				var index int
				_, _ = fmt.Sscanf(answer, "%d", &index)
				if index >= 1 && index <= len(capable) {
					opts.PeerNames = []string{capable[index-1].Name}
				}
			}
		}
	}
	if len(opts.AllowedPeers) == 0 && opts.Role != "origin" {
		value, err := prompt(reader, "Which peers may delegate to this machine? [selected peers or this machine]: ")
		if err != nil {
			return err
		}
		opts.AllowedPeers = splitCSV(value)
	}
	if len(opts.Profiles) == 0 {
		fmt.Println()
		fmt.Println("Profiles to expose:")
		fmt.Println("[x] readonly")
		fmt.Println("[x] dev")
		fmt.Println("[ ] network")
		fmt.Println("[ ] git_push")
		value, err := prompt(reader, "Extra profiles, comma-separated (empty for readonly,dev): ")
		if err != nil {
			return err
		}
		opts.Profiles = append([]string{"readonly", "dev"}, splitCSV(value)...)
	}
	return nil
}

func prompt(reader *bufio.Reader, label string) (string, error) {
	fmt.Print(label)
	text, err := reader.ReadString('\n')
	if err != nil && len(text) == 0 {
		return "", err
	}
	return strings.TrimSpace(text), nil
}

func splitCSV(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func cmeshCapablePeers(peers []onboard.PeerCandidate) []onboard.PeerCandidate {
	var out []onboard.PeerCandidate
	for _, peer := range peers {
		if peer.CmeshCapable {
			out = append(out, peer)
		}
	}
	return out
}

func displayRunner(name string) string {
	switch name {
	case claudecode.Name:
		return "Claude Code"
	case codex.Name:
		return "Codex"
	default:
		return name
	}
}

func printCheck(name string, ok bool, detail string) {
	mark := "x"
	if ok {
		mark = "ok"
	}
	if detail == "" {
		fmt.Printf("%-24s %s\n", name, mark)
		return
	}
	fmt.Printf("%-24s %s  %s\n", name, mark, detail)
}

func firstErrDetail(ok string, err error) string {
	if err != nil {
		return err.Error()
	}
	return ok
}

func configuredPeers(cfg *config.Config) []map[string]any {
	if cfg == nil {
		return nil
	}
	var out []map[string]any
	for name, peer := range cfg.Peers {
		out = append(out, map[string]any{
			"name":    firstNonEmpty(peer.Name, name),
			"node_id": peer.NodeID,
			"address": peer.Address,
			"port":    firstNonZero(peer.Port, config.DefaultMeshPort),
		})
	}
	return out
}

func schemaSmokeRequest(cfg *config.Config, peerName, cwd, profile string, wait bool, timeoutSeconds int) schema.CreateTaskRequest {
	_ = peerName
	return schema.CreateTaskRequest{
		OriginNodeID:    cfg.Node.ID,
		OriginSessionID: "cmesh-cli-smoke",
		Prompt:          "Reply with exactly: " + smokeExpectedResponse,
		CWD:             cwd,
		Profile:         profile,
		TimeoutSeconds:  timeoutSeconds,
		Wait:            wait,
		Provenance: schema.Provenance{
			OriginNodeID:    cfg.Node.ID,
			OriginSessionID: "cmesh-cli-smoke",
			DelegationChain: []string{cfg.Node.ID},
			HopCount:        0,
		},
	}
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func firstNonZero(value, fallback int) int {
	if value != 0 {
		return value
	}
	return fallback
}

func isTerminal(file *os.File) bool {
	if file == nil {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func usage() {
	fmt.Println("usage: cmesh <init|doctor|up|peers|test|mcp>")
	fmt.Println()
	fmt.Println("common:")
	fmt.Println("  cmesh init")
	fmt.Println("  cmesh doctor")
	fmt.Println("  cmesh up")
	fmt.Println("  cmesh peers")
	fmt.Println("  cmesh peers test <peer> --cwd <peer-cwd> --bidirectional")
	fmt.Println("  cmesh mcp")
}
