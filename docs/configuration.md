# Configuration

> Experimental: cmesh can cause real changes on real machines. Keep configs narrow, assume worker profiles grant real authority, and do not use this on anything you love: machines, repos, credentials, or data you cannot afford to lose.

cmesh uses a TOML config file, but **most users never write it by hand**. The
recommended path is to register the cmesh MCP and let the onboarding wizard
generate, confirm, and write your config:

```bash
cmesh install            # put the binary on PATH + register the MCP into your agent
# then, in a new agent session: "help me setup cmesh"
```

The wizard detects Tailscale, local runners, and cmesh-capable peers, shows you the
result, and writes the config only after you confirm. See [Quickstart](quickstart.md)
for the full flow. This document is the reference for the file it produces — read it
when you want to understand a field, automate config generation, or hand-tune an
escape hatch.

By default, cmesh reads:

```text
~/.config/cmesh/config.toml
```

You can pass a specific file with:

```bash
cmesh up --config /path/to/config.toml
```

For scripted, non-interactive setup, `cmesh init` writes a config from flags
(`--role`, `--allow-peer`, `--allow-cwd`, `--peer`, `--profile`) without the wizard.
Prefer the wizard for interactive setup; reach for `init` in automation.

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
- `profile_ceilings`: optional map from a **requested** profile name to a **ceiling** profile name. When a peer requests the keyed profile, the worker silently remaps the effective profile down to the ceiling (e.g. `profile_ceilings = { system-change = "repo-dev" }` caps any `system-change` request to `repo-dev`). The ceiling profile must itself be configured.

`[policy.outbound]` is enforced by the origin daemon before dispatch:

```toml
[policy.outbound]
allowed_peers = ["homelab-vm"]
denied_peers = ["snapshot-vm"]
denied_profiles = ["network", "interactive", "git_push"]
allow_subdelegation = false
```

- `allowed_peers` / `denied_peers`: origin-side allowlist and denylist of peers this machine may dispatch to. A peer matched by `denied_peers` is rejected with `peer_unauthorized`; a non-empty `allowed_peers` rejects any peer not on it.
- `denied_profiles`: an origin-side brake. Generated configs keep high-risk profiles in this list, even if those profiles are defined locally, so they cannot be requested accidentally. Remove a profile from this list only on an origin machine that is intentionally allowed to request it.
- `allow_subdelegation`: whether a worker on this machine may delegate onward to another peer.

## Profiles

Profiles are permission bundles. The generated default is `query`, which uses
the built-in probe runner rather than Claude Code or Codex:

```toml
[profiles.query]
runner = "probe"
intents = ["query"]
capabilities = ["disk_info", "system_info", "repo_status"]
writes = "deny"
shell = "deny"
network = false
max_runtime = "30s"
enforcement_confidence = "high"
```

[Runner manifests](runner-manifests.md) declare what those claims mean for a specific harness.

Defaults expose `query`; agent-backed profiles such as `repo-dev` must be added explicitly and requested by name. High-risk profiles such as `desktop-control`, `system-change`, `network`, `interactive`, and `git_push` must also be explicit. For profiles that should only be usable by specific peers, set `allowed_peers` on the profile:

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

### Profile authority ladder

The built-in templates climb from deterministic read-only probes to high-risk agent-driven authority. The columns show which rungs must be requested by name (`require_explicit`) and which high-risk rungs additionally require a worker-issued lease (`require_lease`). The worker can also cap a request to a lower rung with `profile_ceilings`.

| Profile | Runner | Risk | In default config | `require_explicit` | `require_lease` |
| --- | --- | --- | --- | --- | --- |
| `query` | `probe` | low | yes | no | no |
| `query-network` | `probe` | medium | opt-in | yes | no |
| `query-shell` | `shell` | medium | opt-in | yes | no |
| `readonly-agent` | agent | medium | opt-in | no | no |
| `repo-dev` | agent | high | opt-in | yes | yes |
| `desktop-control` | agent | high | opt-in | yes | yes |
| `system-change` | agent | high | opt-in | yes | yes |

Generated configs expose `query` only; every higher profile is added deliberately and, for high-risk profiles, kept in `[policy.outbound].denied_profiles` on origins that should not request them. See [Security](security.md) for the reasoning behind keeping this ladder narrow.

### Shell policy

Allowlisted-shell profiles (the `shell` runner, e.g. `query-shell`) constrain
which commands may run via `[profiles.X.shell_policy]`:

```toml
[profiles.query-shell.shell_policy]
mode = "allowlist"
denied_patterns = [">", ">>", "|", "&&", "||", ";", "`", "$(", " rm "]

[profiles.query-shell.shell_policy.allowed_commands]
darwin = ["df", "uname", "sysctl", "ps", "log"]
linux = ["df", "uname", "systemctl", "ps", "journalctl"]
windows = ["Get-Service", "Get-Process", "Get-EventLog", "Get-PSDrive"]
```

- `mode = "allowlist"` runs only the listed commands.
- `allowed_commands` is keyed by OS (`darwin`, `linux`, `windows`); an empty key applies to all.
- `denied_patterns` rejects shell metacharacters and dangerous tokens even inside an allowlisted command.

## Runners

Runner config points to the local command:

```toml
[runners.claude-code]
command = ["claude", "-p"]
default_profile = "dev"
concurrency = 1
```

For services, prefer absolute runner paths so the service account does not depend on an interactive shell `PATH`. On Windows workers, prefer the current-user Scheduled Task mode when the runner auth belongs to the interactive user:

```powershell
.\cmesh.exe up service install --user-task
.\cmesh.exe up service start
```

That task survives SSH logout and keeps Claude Code or Codex credentials in the same user profile. If you need a traditional SCM service instead, install it under the same authenticated user account used by the runner:

```powershell
.\cmesh.exe up service install --user ".\scott" --prompt-password
```

Example Windows runner command:

```toml
[runners.claude-code]
command = ["C:\\Users\\scott\\AppData\\Roaming\\npm\\claude.cmd", "-p"]
```

Codex write-capable profiles default to `workspace-write`. On Windows, Codex may report `workspace-write` while still starting read-only; use `allow_danger_full_access = true` only when the worker is already isolated by the OS account and cmesh profile leases:

```toml
[runners.codex]
command = ["C:\\Users\\scott\\.local\\bin\\codex.cmd"]
default_profile = "repo-dev"
allow_danger_full_access = true
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

`name` and `node_id` must be the peer's **whois-matchable identity**: its Tailscale DNS hostname label (e.g. `homelab-vm`, the first label of `homelab-vm.tailnet.ts.net`) or its stable node ID. This is what Tailscale `whois` reports for an incoming connection and therefore what the inbound policy (and `allowed_peers`) matches against. Do **not** use a peer's human display name (e.g. "Scott's MacBook Pro") — that is not whois-matchable, so the worker will reject the peer with `peer_unauthorized` even though the entry "looks" right.

The wizard writes the correct identity for you, and `repair_peers` reconciles an existing entry against the live `whois` identity (see [Quickstart](quickstart.md)). Hand-edit these blocks only as an escape hatch, and prefer the DNS label or stable ID.

## See also

- [Security](security.md) — why policy, profiles, and peer identity are defined the way they are.
- [MCP onboarding tools](mcp-onboarding.md) — the wizard tools that generate and write this config for you.
- [Runner manifests](runner-manifests.md) — how profile claims map to what a specific runner actually enforces.
