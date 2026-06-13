# Configuration

cmesh uses a TOML config file, but most users should start with:

```bash
cmesh init
```

`init` detects Tailscale, local runners, and cmesh-capable peers, then writes the config for you. Edit TOML directly when you need an escape hatch or automation. By default, cmesh reads:

```text
~/.config/cmesh/config.toml
```

You can pass a specific file with:

```bash
cmesh up --config /path/to/config.toml
```

For agent-assisted setup before the daemon exists, run:

```bash
cmesh mcp
```

It detects supported AI coding agents and upserts a stdio setup MCP entry that launches `cmesh mcp --stdio`. That setup MCP can generate, validate, and write config after explicit confirmation.

## Minimal shape

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

[peers.mac-studio]
name = "mac-studio"
node_id = "mac-studio"
address = "100.64.10.10"
port = 7778
```

## Node

`[node]` describes the local daemon:

- `id`: stable node identifier used in policy, provenance, and audit.
- `name`: human-readable name.
- `mesh_port`: daemon-to-daemon RPC port.
- `mcp_port`: local loopback MCP gateway port.
- `data_dir`: optional runtime directory for SQLite, logs, and audit defaults.

## Tailscale

`[tailscale]` controls how mesh RPC binds:

- `mode = "host"` uses the host `tailscaled`.
- `bind = "tailnet_ip"` discovers the node's Tailscale IP and binds only there.

The mesh server refuses wildcard bind addresses.

## Policy

`[policy]` is the worker-side trust boundary:

- `allowed_peers`: node ids or names allowed to delegate to this worker.
- `allowed_cwds`: cwd globs evaluated after home expansion and realpath normalization.
- `hop_limit`: maximum delegation hop count.

`[policy.outbound]` is enforced by the origin daemon before dispatch:

```toml
[policy.outbound]
allowed_peers = ["homelab-vm"]
denied_profiles = ["yolo"]
allow_subdelegation = false
```

## Profiles

Profiles are model-facing permission bundles:

```toml
[profiles.readonly]
runner = "claude-code"
claims = ["read_files"]
bare = true
network = false
git_push = false
max_runtime = "30m"
```

Runner manifests declare what those claims mean for a specific harness.

## Runners

Runner config points to the local command:

```toml
[runners.claude-code]
command = ["claude", "-p"]
default_profile = "dev"
concurrency = 1
```

Supported runner adapters:

- `claude-code`
- `codex`

## Gateway

`[gateway]` controls the local loopback MCP gateway:

```toml
[gateway]
host_token = "local-random-secret"
```

When `host_token` is set, the gateway rejects tokenless local MCP requests. Claude Code worker subdelegation still works because cmesh writes a per-task MCP config with a bearer task token and launches the runner with that config. Task tokens are removed when the worker task reaches a terminal state.

## Peers

Peers are static in the current implementation:

```toml
[peers.homelab-vm]
name = "homelab-vm"
node_id = "homelab-vm"
address = "100.64.10.20"
port = 7778
```

Use the peer's Tailscale IP for `address`.
