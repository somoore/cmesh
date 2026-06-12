# Quickstart

This guide gets two machines talking over Tailscale with static peer config.

## 1. Prerequisites

Install and sign in to Tailscale on each machine:

```bash
tailscale up
```

Install at least one supported worker runner on each machine that will execute tasks:

- Claude Code for the `claude-code` runner
- Codex CLI for the `codex` runner

Make sure the runner works non-interactively for the profile you plan to expose.

## 2. Build cmesh

From the repo root:

```bash
go build -o cmesh ./cmd/cmesh
```

On Windows PowerShell:

```powershell
go build -o cmesh.exe ./cmd/cmesh
```

## 3. Create a config

Start from the examples:

```bash
mkdir -p ~/.config/cmesh
cp examples/config/homelab-vm.toml ~/.config/cmesh/config.toml
```

Edit the config for each machine:

- `[node]` names the current machine.
- `[policy]` controls who may delegate here and which working directories are allowed.
- `[profiles.*]` maps model-facing profiles to runner claims.
- `[runners.*]` defines the local runner command.
- `[peers.*]` lists static peer addresses and mesh ports.

Use the peer's Tailscale IP for `address`.

## 4. Check Tailscale access

Run this on each node:

```bash
./cmesh doctor tailscale
```

Expected shape:

```text
tailscaled: Running
localapi: reachable
tailnet_ip: 100.x.y.z
whois_self: <node>
bind_tailnet_ip: ok
```

If this fails under the account that will run the daemon, fix Tailscale LocalAPI access before continuing.

## 5. Start the daemon

Run on each peer:

```bash
./cmesh up --config ~/.config/cmesh/config.toml
```

cmesh starts two listeners:

- mesh RPC on the node's Tailscale IP and configured mesh port;
- local MCP gateway on `127.0.0.1:<mcp_port>/mcp`.

## 6. Register the local gateway

On the machine where your agent session runs:

```bash
./cmesh connect --config ~/.config/cmesh/config.toml
```

For Claude Code, cmesh attempts:

```bash
claude mcp add cmesh http://127.0.0.1:7777/mcp
```

For other harnesses, register a local HTTP MCP server named `cmesh` at:

```text
http://127.0.0.1:7777/mcp
```

## 7. Delegate work

Ask your local agent to delegate a self-contained task:

```text
Use cmesh to delegate the Linux build to homelab-vm from ~/code/myrepo and keep working locally.
```

The default flow returns a task id immediately. Later, the agent can call:

- `task_status(task_id)`
- `task_result(task_id)`
- `cancel(task_id)`

