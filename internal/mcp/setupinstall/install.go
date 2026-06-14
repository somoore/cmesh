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

func ClaudeHTTPAddArgs(serverName, gatewayURL, hostToken string) []string {
	if serverName == "" {
		serverName = ServerName
	}
	args := []string{"mcp", "add", "--transport", "http", serverName, gatewayURL}
	if strings.TrimSpace(hostToken) != "" {
		args = append(args, "--header", "X-Cmesh-Host-Token: "+strings.TrimSpace(hostToken))
	}
	return args
}

func RedactClaudeHTTPAddArgs(args []string) []string {
	redacted := append([]string(nil), args...)
	for i, arg := range redacted {
		if strings.HasPrefix(arg, "X-Cmesh-Host-Token: ") {
			redacted[i] = "X-Cmesh-Host-Token: <redacted>"
		}
	}
	return redacted
}

func ShellCommand(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if !strings.ContainsAny(value, " \t\n'\"\\$`!|&;<>(){}[]*?~#") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
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
		if name, ok := tomlTableHeader(line); ok {
			skip = name == table || strings.HasPrefix(name, table+".")
		}
		if !skip {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// tomlTableHeader parses a TOML table header line and returns the table name.
// It recognizes a header as a trimmed line starting with "[" whose first "]"
// closes the table name, allowing an optional trailing "# comment" after the
// closing bracket. Array-of-tables headers ([[...]]) are preserved as-is by
// trimming the matching bracket pair around the name.
func tomlTableHeader(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") {
		return "", false
	}
	arrayOfTables := strings.HasPrefix(trimmed, "[[")
	open := "["
	close := "]"
	if arrayOfTables {
		open = "[["
		close = "]]"
	}
	inner := trimmed[len(open):]
	end := strings.Index(inner, close)
	if end < 0 {
		return "", false
	}
	// Anything after the closing bracket must be only whitespace and/or a
	// trailing comment; otherwise this is not a valid table header.
	rest := strings.TrimSpace(inner[end+len(close):])
	if rest != "" && !strings.HasPrefix(rest, "#") {
		return "", false
	}
	return strings.TrimSpace(inner[:end]), true
}

func tomlString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
