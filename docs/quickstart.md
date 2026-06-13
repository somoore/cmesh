# Quickstart

cmesh lets the coding agent on this machine ask the coding agent on another machine to do work.

You install cmesh on each machine. cmesh uses Tailscale to reach peers, verifies who is calling, checks your policy, runs the local agent there, and gives your current agent the result.

## 1. Prerequisites

On each machine:

```bash
tailscale up
```

Install and sign in to at least one supported runner on every machine that should do work:

- Claude Code
- Codex CLI

## 2. Install or build cmesh

From this repo:

```bash
go build -o cmesh ./cmd/cmesh
```

On Windows PowerShell:

```powershell
go build -o cmesh.exe ./cmd/cmesh
```

## 3. Initialize each machine

Run:

```bash
./cmesh init
```

`init` detects Tailscale, local runners, suggested safe directories, and cmesh-capable tailnet peers. It writes:

```text
~/.config/cmesh/config.toml
```

For a headless worker:

```bash
./cmesh init --worker --allow-peer mac-studio --allow-cwd ~/code/** --yes
```

For an origin machine:

```bash
./cmesh init --origin --peer homelab-vm --yes
```

TOML is still available as the escape hatch, but it should not be the first step.

## Agent-assisted first run

You can also let cmesh register the binary MCP server before any config exists:

```text
cmesh mcp
```

`cmesh mcp` detects installed AI coding agents, asks which one should get cmesh, and upserts a stdio MCP entry that launches:

```text
cmesh mcp --stdio
```

That setup MCP exposes read-mostly tools for:

- checking Tailscale and runner readiness;
- listing cmesh-capable tailnet peers;
- generating and validating config;
- writing config only after an explicit confirmation argument.

Once config exists and `cmesh up` is running, use the local HTTP MCP gateway for actual delegation:

```text
http://127.0.0.1:7777/mcp
```

## 4. Check setup

Run:

```bash
./cmesh doctor
```

It checks config, Tailscale, LocalAPI, tailnet binding, runner availability, and the local MCP endpoint.

## 5. Start cmesh

For a foreground daemon:

```bash
./cmesh up
```

For a service:

```bash
./cmesh up service install
./cmesh up service start
./cmesh up service status
```

Service management uses launchd on macOS, systemd user services on Linux, and Windows services on Windows.

On Windows, install the service under the same account that has Tailscale and runner auth when you want Claude Code or Codex to reuse that user's credentials:

```powershell
.\cmesh.exe up service install --user ".\scott" --prompt-password
.\cmesh.exe up service start
```

Keep runner commands absolute in Windows service configs, for example:

```toml
[runners.claude-code]
command = ["C:\\Users\\scott\\AppData\\Roaming\\npm\\claude.cmd", "-p"]
```

If you install without `--user`, the Windows service runs as LocalSystem and will not inherit the interactive user's runner auth or PATH.

## 6. Find and test peers

```bash
./cmesh peers
./cmesh peers test homelab-vm --cwd ~/code/myapp
```

The smoke test checks peer reachability, inbound policy, runner availability, and task completion with a readonly prompt.

## 7. Use it from your agent

Try:

```text
Use cmesh to ask homelab-vm to summarize ~/code/myapp.
```

The local MCP gateway is:

```text
http://127.0.0.1:7777/mcp
```

Use `cmesh mcp` when you want cmesh to install the pre-daemon setup MCP into Claude Code or Codex for you. After `cmesh up` is running, agents should use the local HTTP MCP gateway at the URL above for delegation.

## Optional bootstrap

If SSH is available, an origin machine can install and start cmesh on a worker:

```bash
./cmesh peers bootstrap ssh user@homelab-vm
```

If Tailscale SSH is enabled:

```bash
./cmesh peers bootstrap tailscale homelab-vm
```
