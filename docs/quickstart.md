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

For runner-specific service readiness, run:

```bash
./cmesh doctor runner claude-code
./cmesh doctor service-context
```

`doctor runner claude-code` verifies that the configured runner path exists, is absolute for service use, can authenticate under the current process context, receives the prompt over stdin, and returns a smoke response that can be independently verified from the raw command artifact. `doctor service-context` checks user identity, PATH, home directory, Claude config location, Tailscale LocalAPI access, cwd access, git access, and runner spawning assumptions.

## 5. Start cmesh

For a foreground daemon:

```bash
./cmesh up
```

Startup runs the same runner preflight gate before the mesh listener is advertised. Use `./cmesh up --skip-preflight` only for debugging a broken node locally.

For a service:

```bash
./cmesh up service install
./cmesh up service start
./cmesh up service restart
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
./cmesh peers test homelab-vm --cwd ~/code/myapp --bidirectional
```

The smoke test checks peer reachability, policy, task creation, runner invocation, prompt receipt, result envelope parsing, audit-event recording, and task completion with a readonly prompt. It verifies the returned `command_output` artifact has exit code 0 and contains the expected smoke string, so a runner that exits successfully after saying it did not receive a task, or only returns model prose without a raw command artifact, does not count as healthy.

Use `--bidirectional` when validating a two-machine setup. The first leg runs a direct smoke task from this machine to the peer. The reverse leg asks the peer's cmesh daemon to run its own direct smoke task back to this machine. This avoids routing health checks through Claude Code, Codex, or another agent prompt, so elapsed time reflects cmesh transport and runner startup rather than agent planning latency. If the reverse machine needs a different local path, pass `--reverse-cwd <path-on-this-machine>`.

From the local MCP gateway, the `test_peer` tool runs the same kind of semantic smoke task. It does not stop at capability discovery; it creates a real remote task, fetches the result envelope, verifies the expected raw smoke response, and confirms the origin audit event was written.

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

Direct mesh RPC calls such as `POST http://100.x.y.z:7778/v1/tasks` are for low-level debugging only. Normal user and agent flows should go through the local gateway so origin-side outbound policy and task provenance are applied before the worker sees the request.

## Optional bootstrap

If SSH is available, an origin machine can install and start cmesh on a worker:

```bash
./cmesh peers bootstrap ssh user@homelab-vm
```

If Tailscale SSH is enabled:

```bash
./cmesh peers bootstrap tailscale homelab-vm
```
