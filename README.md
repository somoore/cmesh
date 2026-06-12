# cmesh

**Policy-governed delegation for coding agents across machines you control.**

cmesh turns your laptop, desktop, homelab box, VM, or Windows build target into a small local-first delegation mesh. An agent on one machine can hand a task to an agent on another machine, keep working, then retrieve a durable result with provenance, audit events, and a git branch to review.

The point is simple: coding agents are usually trapped on one machine, but real development work is not.

## Why cmesh exists

Modern coding agents are good at local work, but a single machine is often the wrong place to run the next task.

You may have:

- a Linux box that can run production-like builds;
- a Mac that can build Apple-specific targets;
- a Windows machine for native tests;
- a snapshot VM for risky edits;
- a homelab machine with more CPU, RAM, or the right toolchain;
- separate agent credentials or runner setups per environment.

Without cmesh, delegation usually means manual SSH, copied prompts, lost context, unclear permissions, and no durable task state.

cmesh makes delegation a local tool call:

```text
delegate the Linux build to homelab and keep working
```

Your local agent talks only to the local cmesh gateway. The local daemon enforces outbound policy, sends a typed mesh RPC request over your tailnet, and the remote daemon enforces inbound policy before launching the configured runner.

## What it does

- **Local MCP gateway:** one loopback-only MCP endpoint for agent harnesses.
- **Mesh RPC:** daemon-to-daemon HTTP+JSON over Tailscale.
- **Tailscale identity:** inbound peers are identified with LocalAPI `whois`.
- **Policy boundary:** peer, cwd, runner, profile, hop, and cycle checks happen before execution.
- **Durable tasks:** SQLite stores state, idempotency keys, peer task mirrors, events, and results.
- **Audit trail:** append-only JSONL events plus SQLite event rows.
- **Runner abstraction:** Claude Code and Codex runner adapters live behind one interface.
- **Git handoff:** worker changes land on deterministic `cmesh/<task-id>/<slug>` branches with cmesh metadata footers.
- **Prompt-injection aware results:** worker summaries are untrusted claims; transcripts are referenced, not blindly inlined.
- **Fail-closed restarts:** queued/running worker tasks are marked `worker_daemon_restarted` on daemon startup.

## Current status

cmesh is early, but the repository is structured as a real daemon and CLI, not a mockup. The first useful target is Mac/Linux peer delegation over Tailscale with static peers.

Implemented surfaces include:

- `cmesh up`
- `cmesh connect`
- `cmesh status`
- `cmesh log <task_id>`
- `cmesh doctor tailscale`
- `cmesh hello -url http://100.x.y.z:7778`
- `/mcp` loopback MCP JSON-RPC endpoint
- `/v1/hello`, `/v1/tasks`, `/v1/tasks/{id}`, `/v1/tasks/{id}/result`, `/v1/tasks/{id}/cancel`

## How it works

There are two deliberately separate protocols:

1. **Local MCP:** agent harness to local cmesh daemon, bound to loopback only.
2. **Mesh RPC:** cmesh daemon to cmesh daemon, bound to the node's Tailscale IP.

The orchestrating agent never talks to the remote runner directly. It calls the local gateway. The local daemon calls the peer daemon. The peer daemon launches Claude Code, Codex, or another configured runner locally on that machine.

See [docs/architecture.md](docs/architecture.md) for the end-to-end architecture diagram.

## Quickstart

### 1. Prerequisites

Install and sign in to Tailscale on each machine:

```bash
tailscale up
```

Install at least one supported worker runner on each machine that will execute tasks:

- Claude Code for the `claude-code` runner
- Codex CLI for the `codex` runner

Make sure the runner works non-interactively for the profile you plan to expose.

### 2. Build cmesh

From the repo root:

```bash
go build -o cmesh ./cmd/cmesh
```

On Windows PowerShell:

```powershell
go build -o cmesh.exe ./cmd/cmesh
```

### 3. Create a config

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

Example peer entry:

```toml
[peers.homelab-vm]
name = "homelab-vm"
node_id = "homelab-vm"
address = "100.64.10.20"
port = 7778
```

Use the peer's Tailscale IP for `address`.

### 4. Check Tailscale access

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

### 5. Start the daemon

Run on each peer:

```bash
./cmesh up --config ~/.config/cmesh/config.toml
```

cmesh starts two listeners:

- mesh RPC on the node's Tailscale IP and configured mesh port;
- local MCP gateway on `127.0.0.1:<mcp_port>/mcp`.

### 6. Register the local gateway

On the machine where your agent session runs:

```bash
./cmesh connect --config ~/.config/cmesh/config.toml
```

For Claude Code, cmesh will attempt:

```bash
claude mcp add cmesh http://127.0.0.1:7777/mcp
```

For other harnesses, register a local HTTP MCP server named `cmesh` at:

```text
http://127.0.0.1:7777/mcp
```

### 7. Delegate work

Ask your local agent to delegate a self-contained task:

```text
Use cmesh to delegate the Linux build to homelab-vm from ~/code/myrepo and keep working locally.
```

The default flow returns a task id immediately. Later, the agent can call:

- `task_status(task_id)`
- `task_result(task_id)`
- `cancel(task_id)`

## Security model

cmesh treats network reachability as defense in depth, not authorization.

In Tailscale mode:

- transport and peer identity come from Tailscale;
- cmesh verifies inbound identity with LocalAPI `whois`;
- cmesh policy decides whether that peer may run that task;
- no daemon-to-daemon bearer token is needed;
- mesh RPC must bind only to the node's Tailscale IP;
- local MCP must bind only to loopback.

Worker output is a prompt-injection boundary. The canonical result envelope calls the summary `worker_summary_untrusted` on purpose.

## Configuration sketch

```toml
[node]
id = "homelab-vm"
name = "homelab-vm"
mesh_port = 7778
mcp_port = 7777

[tailscale]
mode = "host"
bind = "tailnet_ip"

[policy]
allowed_peers = ["mac-studio"]
allowed_cwds = ["~/code/**"]
hop_limit = 2

[profiles.dev]
runner = "claude-code"
claims = ["read_files", "edit_repo", "execute_repo_commands"]
bare = false
network = false
git_push = false

[runners.claude-code]
command = ["claude", "-p"]
default_profile = "dev"
```

## Development

Run the test suite:

```bash
go test ./...
```

Format code:

```bash
go fmt ./...
```

Validate example JSON/TOML after editing docs or schemas.

## Project shape

```text
cmd/cmesh                 CLI and daemon entrypoint
internal/audit            JSONL audit events
internal/config           TOML config loading and validation
internal/doctor           environment checks
internal/gitx             git branch, commit, diffstat contract
internal/identity         Tailscale LocalAPI identity
internal/mcp/gateway      loopback MCP gateway
internal/mesh/rpc         daemon-to-daemon RPC server/client
internal/policy           inbound/outbound policy engine
internal/runner           runner abstraction
internal/runner/claudecode Claude Code runner
internal/runner/codex     Codex runner
internal/store            SQLite task store
internal/task             task state and result types
schemas                   JSON Schemas
examples                  runnable config and Tailscale grant examples
docs                      architecture and operator guidance
```
