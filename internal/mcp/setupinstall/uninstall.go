package setupinstall

import (
	"os"
	"os/exec"
	"strings"
)

// DetectInstalledAgents reports which supported agents currently have the cmesh
// MCP server wired up. Unlike DetectAgents (which reports installable agents),
// this only returns agents where cmesh is actually registered:
//
//   - Claude Code: `claude mcp get cmesh` exits 0.
//   - Codex: ~/.codex/config.toml contains a [mcp_servers.cmesh] table.
//
// Agents without cmesh wired are omitted, so the uninstaller can show only the
// agents it can actually act on.
func DetectInstalledAgents() []Agent {
	var agents []Agent
	if a, ok := detectClaudeInstalled(); ok {
		agents = append(agents, a)
	}
	if a, ok := detectCodexInstalled(); ok {
		agents = append(agents, a)
	}
	return agents
}

// Uninstall removes the cmesh MCP registration from the given agent. It mirrors
// Install and is idempotent: removing an entry that is already absent returns
// OK with Detail "not registered".
func Uninstall(agent Agent) Result {
	switch agent.ID {
	case "claude-code":
		return uninstallClaude(agent)
	case "codex":
		return uninstallCodex(agent)
	default:
		return Result{Agent: agent, Detail: "unsupported agent"}
	}
}

func detectClaudeInstalled() (Agent, bool) {
	claude, err := exec.LookPath("claude")
	if err != nil {
		return Agent{}, false
	}
	if !claudeHasCmesh(claude) {
		return Agent{}, false
	}
	return Agent{ID: "claude-code", Name: "Claude Code", Found: true, Detail: "cmesh MCP registered"}, true
}

// claudeScopes are every Claude Code MCP scope cmesh can be registered in. The
// stdio setup server is installed at user scope; register_gateway adds the HTTP
// gateway entry at local (project) scope. `claude mcp get` finds the server in
// ANY scope, but `claude mcp remove` is scope-specific, so an uninstaller that
// only removes one scope leaves the others behind (and a re-run loops forever
// "detecting" a server it cannot remove). We therefore remove from every scope.
var claudeScopes = []string{"user", "local", "project"}

// claudeHasCmesh reports whether the cmesh MCP server is registered with Claude
// Code in any scope. `claude mcp get cmesh` exits 0 when present and non-zero
// when absent.
func claudeHasCmesh(claude string) bool {
	return exec.Command(claude, "mcp", "get", ServerName).Run() == nil
}

func detectCodexInstalled() (Agent, bool) {
	path := CodexConfigPath()
	body, err := os.ReadFile(path)
	if err != nil {
		return Agent{}, false
	}
	if !codexHasCmeshTable(string(body)) {
		return Agent{}, false
	}
	return Agent{ID: "codex", Name: "Codex", Found: true, Detail: path}, true
}

// codexHasCmeshTable reports whether the TOML body declares [mcp_servers.cmesh].
func codexHasCmeshTable(body string) bool {
	want := "[mcp_servers." + ServerName + "]"
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}

func uninstallClaude(agent Agent) Result {
	claude, err := exec.LookPath("claude")
	if err != nil {
		// No claude on PATH: nothing to remove here.
		return Result{Agent: agent, OK: true, Detail: "not registered"}
	}
	if !claudeHasCmesh(claude) {
		return Result{Agent: agent, OK: true, Detail: "not registered"}
	}
	// Sweep every scope cmesh can live in (the stdio setup server installs at
	// user scope; register_gateway adds the HTTP gateway at local/project scope).
	// `claude mcp remove --scope <s>` exits non-zero when cmesh is absent in that
	// scope, so we IGNORE per-scope errors here and let the post-sweep `get` —
	// which is NOT scope-filtered and is the one probe we know works — be the
	// single source of truth for whether anything remains.
	for _, scope := range claudeScopes {
		_ = exec.Command(claude, "mcp", "remove", "--scope", scope, ServerName).Run()
	}
	if claudeHasCmesh(claude) {
		// A real removal failure (or a scope we did not enumerate) left cmesh
		// registered. Surface it rather than reporting a false success — this
		// preserves correctness even if Claude adds a new scope later.
		return Result{Agent: agent, Detail: "cmesh is still registered with Claude Code after removing user/local/project scopes"}
	}
	return Result{Agent: agent, OK: true, Detail: "removed from Claude Code"}
}

func uninstallCodex(agent Agent) Result {
	path := CodexConfigPath()
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{Agent: agent, OK: true, Detail: "not registered"}
		}
		return Result{Agent: agent, Detail: err.Error()}
	}
	if !codexHasCmeshTable(string(body)) {
		return Result{Agent: agent, OK: true, Detail: "not registered"}
	}
	stripped := strings.TrimRight(removeTable(string(body), "mcp_servers."+ServerName), "\r\n")
	if stripped != "" {
		stripped += "\n"
	}
	if err := os.WriteFile(path, []byte(stripped), 0o600); err != nil {
		return Result{Agent: agent, Detail: err.Error()}
	}
	return Result{Agent: agent, OK: true, Detail: "removed from " + path}
}
