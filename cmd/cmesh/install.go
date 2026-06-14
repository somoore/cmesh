package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"cmesh/internal/mcp/setupinstall"
	"cmesh/internal/tui"
)

// cmdInstall is the native interactive installer (Part 2). It detects AI coding
// agents, lets the user pick which to wire the cmesh MCP into, installs the
// stdio setup server, and runs an honest stdio self-test.
//
// `cmesh setup` is an alias for this command.
func cmdInstall(args []string) error {
	_ = args // no flags today; reserved for future --agent / --no-agent-setup
	exe, err := os.Executable()
	if err != nil {
		return err
	}

	all := setupinstall.DetectAgents()
	var detected []setupinstall.Agent
	for _, a := range all {
		if a.Found {
			detected = append(detected, a)
		}
	}

	fmt.Println("cmesh MCP installer")
	fmt.Println()
	if len(detected) == 0 {
		// Part 2 step 2: print install hints and exit non-zero.
		fmt.Println("No supported AI coding agents were detected.")
		fmt.Println()
		fmt.Println("Install one of:")
		fmt.Println("  - Claude Code: https://docs.anthropic.com/en/docs/claude-code")
		fmt.Println("  - Codex:       https://github.com/openai/codex")
		fmt.Println()
		fmt.Println("Then re-run: cmesh install")
		return errors.New("no supported AI coding agents found")
	}

	selected, err := pickAgents("Install the cmesh MCP into which agents?", detected)
	if err != nil {
		if errors.Is(err, tui.ErrCancelled) {
			fmt.Println("Cancelled. No changes made.")
			return nil
		}
		return err
	}
	if len(selected) == 0 {
		fmt.Println("No agents selected. No changes made.")
		return nil
	}

	names := agentNames(selected)
	fmt.Println()
	fmt.Printf("This will install the cmesh MCP into: %s\n", strings.Join(names, ", "))
	ok, err := confirmYesNo("Proceed?", true)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Println("Cancelled. No changes made.")
		return nil
	}

	fmt.Println()
	var failed bool
	for _, agent := range selected {
		result := setupinstall.Install(agent, exe)
		printCheck(agent.Name, result.OK, result.Detail)
		if !result.OK {
			failed = true
		}
	}
	if failed {
		return errors.New("one or more MCP installs failed")
	}

	// Honest stdio self-test: spawn `<exe> mcp --stdio` and run the JSON-RPC
	// handshake. This verifies the MCP is WIRED, not that delegation works
	// (no daemon or peer exists yet).
	fmt.Println()
	if err := selfTestStdio(exe); err != nil {
		printCheck("MCP self-test", false, err.Error())
		fmt.Println()
		fmt.Println("The agent registrations were written, but the stdio self-test did not pass.")
		fmt.Println("You can still try opening a new agent session and running: help me setup cmesh")
		return nil
	}
	printCheck("MCP wired", true, "stdio handshake + tools/list OK")

	fmt.Println()
	fmt.Println("Done. Open a NEW session in your agent and run:")
	fmt.Println()
	fmt.Println("    help me setup cmesh")
	fmt.Println()
	fmt.Println("(`cmesh setup` re-runs this installer.)")
	return nil
}

func agentNames(agents []setupinstall.Agent) []string {
	out := make([]string, 0, len(agents))
	for _, a := range agents {
		out = append(out, a.Name)
	}
	return out
}

// selfTestStdio spawns the cmesh stdio setup MCP server and performs an
// initialize + tools/list handshake over newline-framed JSON-RPC. It returns
// nil only when tools/list reports at least one tool. The child's stderr is
// captured separately so it cannot corrupt the JSON channel, and the child is
// always reaped (kill-on-timeout fallback) so the test never hangs.
func selfTestStdio(exe string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, exe, "mcp", "--stdio")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return err
	}
	// Ensure the child is always reaped.
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()

	reader := bufio.NewReader(stdout)

	// 1) initialize
	if err := writeRPC(stdin, 1, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "cmesh-selftest", "version": "0"},
	}); err != nil {
		return err
	}
	if _, err := readRPCResult(reader); err != nil {
		return fmt.Errorf("initialize: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}

	// 2) tools/list
	if err := writeRPC(stdin, 2, "tools/list", map[string]any{}); err != nil {
		return err
	}
	result, err := readRPCResult(reader)
	if err != nil {
		return fmt.Errorf("tools/list: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) == 0 {
		return errors.New("tools/list returned no tools")
	}
	return nil
}

func writeRPC(w io.Writer, id int, method string, params any) error {
	msg := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	_, err = w.Write(body)
	return err
}

// readRPCResult reads newline-framed JSON-RPC responses until it finds one with
// a "result" object (skipping any notifications) and returns that result map.
func readRPCResult(reader *bufio.Reader) (map[string]any, error) {
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if len(strings.TrimSpace(line)) == 0 {
				return nil, err
			}
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if err != nil {
				return nil, err
			}
			continue
		}
		var resp struct {
			Result map[string]any  `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if jerr := json.Unmarshal([]byte(trimmed), &resp); jerr != nil {
			// Not a JSON line (e.g. a stray banner); keep reading.
			if err != nil {
				return nil, err
			}
			continue
		}
		if len(resp.Error) > 0 {
			return nil, fmt.Errorf("rpc error: %s", string(resp.Error))
		}
		if resp.Result != nil {
			return resp.Result, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// stdinReader is a single shared buffered reader over os.Stdin. Re-wrapping
// os.Stdin per prompt can drop bytes a previous reader buffered, so the
// install/uninstall flows thread this one reader through every prompt.
var stdinReader = bufio.NewReader(os.Stdin)

// confirmYesNo prompts for a Y/N answer using the shared stdin reader. On a
// non-interactive stdin (EOF) it returns the supplied default.
func confirmYesNo(question string, def bool) (bool, error) {
	suffix := " [y/N]: "
	if def {
		suffix = " [Y/n]: "
	}
	fmt.Print(question + suffix)
	line, err := stdinReader.ReadString('\n')
	if err != nil && line == "" {
		// EOF / non-interactive: fall back to the default.
		return def, nil
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	switch answer {
	case "":
		return def, nil
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, nil
	}
}
