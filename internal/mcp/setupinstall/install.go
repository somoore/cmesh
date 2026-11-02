package setupinstall

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
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

// ClaudeConfigPath is Claude Code's user-scope config file. User-scope MCP
// servers live under its top-level mcpServers object.
func ClaudeConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".claude.json"
	}
	return filepath.Join(home, ".claude.json")
}

// WriteClaudeHTTPConfig registers the cmesh HTTP MCP server in Claude Code's
// user-scope config without ever putting the host token in a process argument
// list. `claude mcp add --header "...token..."` exposed the long-lived token to
// `ps`/`/proc/<pid>/cmdline`; this writes the same entry the CLI would persist,
// at 0600, and leaves every other key and server untouched.
func WriteClaudeHTTPConfig(path, serverName, gatewayURL, hostToken string) error {
	if path == "" {
		path = ClaudeConfigPath()
	}
	if serverName == "" {
		serverName = ServerName
	}
	root := map[string]json.RawMessage{}
	if existing, err := os.ReadFile(path); err == nil {
		if strings.TrimSpace(string(existing)) != "" {
			if err := json.Unmarshal(existing, &root); err != nil {
				return fmt.Errorf("%s is not a JSON object: %w", path, err)
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	servers := map[string]json.RawMessage{}
	if raw, ok := root["mcpServers"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return fmt.Errorf("%s: mcpServers is not an object: %w", path, err)
		}
	}
	entry := map[string]any{"type": "http", "url": gatewayURL}
	if token := strings.TrimSpace(hostToken); token != "" {
		entry["headers"] = map[string]string{"X-Cmesh-Host-Token": token}
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	servers[serverName] = encoded
	serversJSON, err := json.Marshal(servers)
	if err != nil {
		return err
	}
	root["mcpServers"] = serversJSON
	body, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return writeJSON0600(path, append(body, '\n'))
}

// writeJSON0600 replaces path atomically with a private file, so a crash cannot
// leave a truncated harness config and a pre-existing mode cannot leak it.
func writeJSON0600(path string, body []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".cmesh-claude-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
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
	// A stale local HTTP gateway entry overrides the user-scoped stdio setup
	// server inside that project and shows up in Claude as reconnecting when no
	// daemon is running yet. Modern Claude uses local for that private scope;
	// older Claude called the same private scope project. Do not remove modern
	// project scope because that is shared .mcp.json configuration.
	for _, scope := range claudeInstallCleanupScopes(claude) {
		_ = exec.Command(claude, "mcp", "remove", "--scope", scope, ServerName).Run()
	}
	cmd := exec.Command(claude, "mcp", "add", "--scope", "user", ServerName, "--", cmeshPath, "mcp", "--stdio")
	out, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			detail = err.Error()
		}
		return Result{Agent: agent, Detail: detail}
	}
	if !claudeResolvesToSetupStdio(claude, cmeshPath) {
		return Result{Agent: agent, Detail: "Claude Code still resolves cmesh to a different MCP entry after install; remove or rename any project-scoped cmesh entry and retry"}
	}
	return Result{Agent: agent, OK: true, Detail: "registered with Claude Code"}
}

// claudeInstallCleanupScopes are private scopes the per-user installer may
// replace automatically. Current Claude Code uses "local" for private
// per-project entries and "project" for VCS-shared .mcp.json. Older Claude Code
// versions used "project" for the private per-project scope, before "local"
// existed; keep cleaning that legacy private scope without touching modern
// shared project config.
func claudeInstallCleanupScopes(claude string) []string {
	scopes := []string{"user", "local"}
	out, err := exec.Command(claude, "mcp", "add", "--help").CombinedOutput()
	if err != nil {
		return scopes
	}
	help := strings.ToLower(string(out))
	if strings.Contains(help, "--scope") && !strings.Contains(help, "local") {
		scopes = append(scopes, "project")
	}
	return scopes
}

func claudeResolvesToSetupStdio(claude, cmeshPath string) bool {
	out, err := exec.Command(claude, "mcp", "get", ServerName).CombinedOutput()
	if err != nil {
		return false
	}
	return claudeGetOutputIsSetupStdio(string(out), cmeshPath)
}

func claudeGetOutputIsSetupStdio(body, cmeshPath string) bool {
	lower := strings.ToLower(body)
	if !strings.Contains(lower, "scope: user") ||
		claudeGetOutputHasBlockedStatus(body) ||
		strings.Contains(lower, "http://") ||
		strings.Contains(lower, "https://") {
		return false
	}
	return strings.Contains(body, cmeshPath) &&
		strings.Contains(body, "mcp") &&
		strings.Contains(body, "--stdio")
}

func claudeGetOutputHasBlockedStatus(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		lower := strings.ToLower(strings.TrimSpace(line))
		if i := strings.Index(lower, ":"); i >= 0 {
			label := strings.TrimSpace(lower[:i])
			if label == "status" || label == "approval" {
				value := strings.TrimSpace(lower[i+1:])
				if strings.Contains(value, "pending") || strings.Contains(value, "rejected") {
					return true
				}
			}
		}
		status := strings.TrimLeftFunc(lower, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsNumber(r)
		})
		if strings.HasPrefix(status, "pending approval") || strings.HasPrefix(status, "rejected") {
			return true
		}
	}
	return false
}

func installCodex(agent Agent, cmeshPath string) Result {
	if !agent.Found {
		return Result{Agent: agent, Detail: "Codex was not found"}
	}
	if codex, err := exec.LookPath("codex"); err == nil {
		_ = exec.Command(codex, "mcp", "remove", ServerName).Run()
		cmd := exec.Command(codex, "mcp", "add", ServerName, "--", cmeshPath, "mcp", "--stdio")
		out, err := cmd.CombinedOutput()
		if err != nil {
			detail := strings.TrimSpace(string(out))
			if detail == "" {
				detail = err.Error()
			}
			return Result{Agent: agent, Detail: detail}
		}
		if !codexHasCmeshCLI(codex) {
			return Result{Agent: agent, Detail: "Codex MCP registration did not verify after install"}
		}
		return Result{Agent: agent, OK: true, Detail: "registered with Codex"}
	}
	path := CodexConfigPath()
	if err := UpsertCodexConfig(path, cmeshPath); err != nil {
		return Result{Agent: agent, Detail: err.Error()}
	}
	return Result{Agent: agent, OK: true, Detail: fmt.Sprintf("updated %s (Codex CLI not found)", path)}
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
