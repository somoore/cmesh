# Configuration

> Experimental: cmesh can cause real changes on real machines. Keep configs narrow, assume worker profiles grant real authority, and do not use this on anything you love: machines, repos, credentials, or data you cannot afford to lose.

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
denied_profiles = ["network", "interactive", "git_push"]
allow_subdelegation = false
```

`denied_profiles` is an origin-side brake. Generated configs keep high-risk profiles in this list, even if those profiles are defined locally, so they cannot be requested accidentally. Remove a profile from this list only on an origin machine that is intentionally allowed to request it.

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

Defaults expose only `readonly` and `dev`. High-risk profiles such as `network`, `interactive`, and `git_push` must be added explicitly and requested by name. For profiles that should only be usable by specific peers, set `allowed_peers` on the profile:

```toml
[profiles.interactive]
runner = "codex"
claims = ["read_files", "edit_repo", "execute_repo_commands", "network", "launch_processes", "desktop_interaction"]
allowed_peers = ["scotts-macbook-pro"]
require_explicit = true
bare = false
network = true
git_push = false
max_runtime = "30m"
```

Profile-level `allowed_peers` is checked in addition to `[policy].allowed_peers` and `[policy.outbound].allowed_peers`. Use it for interactive or network-capable profiles so a generally trusted peer cannot automatically use a more powerful profile. `require_explicit = true` prevents a high-risk profile from being selected through an empty profile request, even if someone accidentally makes it the runner default.

## Runners

Runner config points to the local command:

```toml
[runners.claude-code]
command = ["claude", "-p"]
default_profile = "dev"
concurrency = 1
```

For services, prefer absolute runner paths so the service account does not depend on an interactive shell `PATH`. On Windows, install the service under the same authenticated user account used by the runner:

```powershell
.\cmesh.exe up service install --user ".\scott" --prompt-password
```

Example Windows runner command:

```toml
[runners.claude-code]
command = ["C:\\Users\\scott\\AppData\\Roaming\\npm\\claude.cmd", "-p"]
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
