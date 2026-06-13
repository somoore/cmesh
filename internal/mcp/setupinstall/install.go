package setupinstall

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const ServerName = "cmesh"

type Agent struct {
	ID     string
	Name   string
	Found  bool
	Detail string
}

type Result struct {
	Agent  Agent
	OK     bool
	Detail string
}

func DetectAgents() []Agent {
	return []Agent{
		detectCommand("claude-code", "Claude Code", "claude"),
		detectCodex(),
	}
}

func Install(agent Agent, cmeshPath string) Result {
	switch agent.ID {
	case "claude-code":
		return installClaude(agent, cmeshPath)
	case "codex":
		return installCodex(agent, cmeshPath)
	default:
		return Result{Agent: agent, Detail: "unsupported agent"}
	}
}

func ClaudeHTTPAddArgs(serverName, gatewayURL string) []string {
	if serverName == "" {
		serverName = ServerName
	}
	return []string{"mcp", "add", "--transport", "http", serverName, gatewayURL}
}

func UpsertCodexConfig(path, cmeshPath string) error {
	if path == "" {
		path = CodexConfigPath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body := ""
	if existing, err := os.ReadFile(path); err == nil {
		body = string(existing)
	} else if !os.IsNotExist(err) {
		return err
	}
	body = strings.TrimRight(removeTable(body, "mcp_servers."+ServerName), "\r\n")
	if body != "" {
		body += "\n\n"
	}
	body += "[mcp_servers." + ServerName + "]\n"
	body += "command = " + tomlString(cmeshPath) + "\n"
	body += "args = [\"mcp\", \"--stdio\"]\n"
	body += "startup_timeout_sec = 120\n"
	return os.WriteFile(path, []byte(body), 0o600)
}

func CodexConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.toml"
	}
	return filepath.Join(home, ".codex", "config.toml")
}

func detectCommand(id, name, command string) Agent {
	path, err := exec.LookPath(command)
	if err != nil {
		return Agent{ID: id, Name: name, Detail: "not found on PATH"}
	}
	return Agent{ID: id, Name: name, Found: true, Detail: path}
}

func detectCodex() Agent {
	agent := detectCommand("codex", "Codex", "codex")
	if agent.Found {
		return agent
	}
	if _, err := os.Stat(CodexConfigPath()); err == nil {
		return Agent{ID: "codex", Name: "Codex", Found: true, Detail: CodexConfigPath()}
	}
	return agent
}

func installClaude(agent Agent, cmeshPath string) Result {
	if !agent.Found {
		return Result{Agent: agent, Detail: "Claude Code was not found"}
	}
	claude, err := exec.LookPath("claude")
	if err != nil {
		return Result{Agent: agent, Detail: err.Error()}
	}
	_ = exec.Command(claude, "mcp", "remove", "--scope", "user", ServerName).Run()
	cmd := exec.Command(claude, "mcp", "add", "--scope", "user", ServerName, "--", cmeshPath, "mcp", "--stdio")
	out, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			detail = err.Error()
		}
		return Result{Agent: agent, Detail: detail}
	}
	return Result{Agent: agent, OK: true, Detail: "registered with Claude Code"}
}

func installCodex(agent Agent, cmeshPath string) Result {
	if !agent.Found {
		return Result{Agent: agent, Detail: "Codex was not found"}
	}
	path := CodexConfigPath()
	if err := UpsertCodexConfig(path, cmeshPath); err != nil {
		return Result{Agent: agent, Detail: err.Error()}
	}
	return Result{Agent: agent, OK: true, Detail: fmt.Sprintf("updated %s", path)}
}

func removeTable(body, table string) string {
	var kept []string
	lines := strings.Split(body, "\n")
	skip := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			name := strings.Trim(trimmed, "[]")
			skip = name == table || strings.HasPrefix(name, table+".")
		}
		if !skip {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func tomlString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
