# Command reference

> Experimental: cmesh can cause real changes on real machines. Do not use it on anything you love: machines, repos, credentials, or data you cannot afford to lose. Start on trusted private networks with narrow peer allowlists, narrow cwd allowlists, and the `query` profile.

The point of cmesh is that you rarely touch the CLI. The only real install is the binary plus the MCP registration, and from there an agent-driven wizard does the rest (see [Quickstart](quickstart.md)). This page is the complete reference for the times you do run a command by hand: first-run install, diagnostics, and direct operation of a node.

Commands are grouped into the small **primary surface** the project supports as the everyday interface, and an **auxiliary surface** of headless, low-level, and internal commands that exist for CI, scripting, and debugging.

Common conventions:

- Most commands accept `--config <path>` to point at a config other than the default `~/.config/cmesh/config.toml`.
- Many read commands accept `--json` to print machine-readable output instead of a table.
- Flags may appear before or after positional arguments.

## Primary commands

The supported top-level surface, as printed by `cmesh` with no arguments:

```text
usage: cmesh <install|uninstall|up|doctor|peers|profile|mcp>
```

### `cmesh install` (alias: `cmesh setup`)

Detect the AI coding agents on this machine (Claude Code, Codex), let you pick which ones to wire the cmesh MCP into, register a stdio MCP entry that launches `cmesh mcp --stdio`, and run an honest stdio self-test (a JSON-RPC `initialize` + `tools/list` handshake against the freshly registered server). It first puts the binary on your PATH unless told not to.

| Flag | Description |
|---|---|
| `--no-self-install` | Skip the PATH setup step. Use when a package manager owns the binary location. |

`cmesh setup` is an exact alias; run it to re-open the agent picker later, for example after installing a new agent.

```bash
go build -o cmesh ./cmd/cmesh
./cmesh install
```

After it finishes, open a new agent session and say `help me setup cmesh`.

### `cmesh uninstall`

Mirror of the installer. Detect which agents currently have a cmesh MCP registered, show only those, let you multi-select which to remove, confirm, and remove the MCP entries. It then offers separately consented cleanup of the rest — the OS service, the config file, the data directory (database, audit logs, artifacts, worktrees — removing it deletes audit history), and the self-installed binary and PATH alias. Each destructive step is its own Y/N prompt, never bundled. It is safe to run twice and never deletes anything cmesh did not create.

Takes no flags.

```bash
cmesh uninstall
```

### `cmesh up`

Run the cmesh daemon in the foreground. Startup runs a runner preflight (a local smoke round-trip through each configured runner) before binding the mesh listener, so a node that cannot resolve, authenticate, or feed prompts to its runner fails before advertising itself as reachable. The daemon serves the local HTTP MCP gateway at `http://127.0.0.1:7777/mcp` and the mesh RPC server on the node's Tailscale IP (`:7778`).

| Flag | Description |
|---|---|
| `--config <path>` | Path to cmesh config. Default `~/.config/cmesh/config.toml`. |
| `--skip-preflight` | Skip the runner startup self-checks. For debugging a broken node locally only. |

```bash
cmesh up
cmesh up --skip-preflight   # debugging a broken node only
```

Normally the daemon runs as the OS service the wizard installed, not in the foreground. Use the `service` subcommand to manage it.

#### `cmesh up service <install|start|stop|restart|status|logs>`

Manage the cmesh background service (launchd on macOS, a systemd user service on Linux, a Windows service on Windows).

```text
usage: cmesh up service <install|start|stop|restart|status|logs>
```

| Subcommand | Description |
|---|---|
| `install` | Install the OS service. |
| `start` | Start the service. |
| `stop` | Stop the service. |
| `restart` | Restart the service (use this to reload config after a repair). |
| `status` | Show the service status. |
| `logs` | Show the service logs. |

`install` accepts Windows service-account flags (ignored on macOS and Linux). A service installed without an explicit user runs as LocalSystem and will not inherit the interactive user's runner auth or PATH.

| Flag (install) | Description |
|---|---|
| `--config <path>` | Path to cmesh config. |
| `--user <account>` | Windows service account, such as `.\scott` or `DOMAIN\user`. |
| `--password <pw>` | Windows service account password. Prefer `--prompt-password` or `--password-env`. |
| `--password-env <var>` | Environment variable holding the Windows service account password. |
| `--prompt-password` | Prompt for the Windows service account password. |

```bash
cmesh up service install
cmesh up service status
cmesh up service restart
```

### `cmesh doctor`

Check this node's setup: config, Tailscale identity and reachability, LocalAPI, tailnet binding, runner availability, and the local MCP gateway. Run it under the same account or service user that runs the daemon.

| Flag | Description |
|---|---|
| `--config <path>` | Path to cmesh config. |
| `--json` | Print JSON. |

```bash
cmesh doctor
```

`doctor` routes to a specialized check when its first argument names one:

#### `cmesh doctor tailscale`

Check Tailscale identity and the mesh bind behavior.

| Flag | Description |
|---|---|
| `--mesh-port <n>` | Mesh RPC port to check. Default is the configured mesh port. |

#### `cmesh doctor runner <runner>`

Deep-check one runner: that it resolves, authenticates, and answers a smoke prompt over stdin.

```text
usage: cmesh doctor runner <runner> [--config path] [--skip-smoke]
```

| Flag | Description |
|---|---|
| `--config <path>` | Path to cmesh config. |
| `--json` | Print JSON. |
| `--skip-smoke` | Skip invoking the runner smoke prompt. |
| `--timeout <seconds>` | Runner smoke timeout in seconds. Default 60. |

```bash
cmesh doctor runner claude-code
```

#### `cmesh doctor profile <profile>`

Describe one configured profile and check it against the available runner.

```text
usage: cmesh doctor profile <profile> [--config path]
```

| Flag | Description |
|---|---|
| `--config <path>` | Path to cmesh config. |
| `--json` | Print JSON. |

#### `cmesh doctor service-context`

Check that the daemon's service account has what it needs: runner auth, Tailscale LocalAPI access, and PATH. Run it under the service account to confirm a worker will behave the same way the service does.

| Flag | Description |
|---|---|
| `--config <path>` | Path to cmesh config. |
| `--json` | Print JSON. |

```bash
cmesh doctor service-context
```

### `cmesh peers`

List configured peers and, by default, tailnet candidates that may expose cmesh.

| Flag | Description |
|---|---|
| `--config <path>` | Path to cmesh config. |
| `--discover` | Also show tailnet peer candidates. Default true; pass `--discover=false` to skip discovery. |
| `--json` | Print JSON. |

```bash
cmesh peers
```

#### `cmesh peers test <peer>`

Run a real smoke task against a peer: create a readonly task, fetch the result envelope, and verify the expected raw smoke response and audit event. A runner that exits successfully without actually doing the task does not count as healthy. With no `--cwd`, the worker picks a directory its own `allowed_cwds` permits, so the smoke works across machines and operating systems without you knowing a path on the other side.

```text
usage: cmesh test <peer> [--cwd ~/code/myapp] [--bidirectional]
```

| Flag | Description |
|---|---|
| `--config <path>` | Path to cmesh config. |
| `--cwd <path>` | Worker cwd for the smoke task. Empty: the worker picks an allowed dir. |
| `--profile <name>` | Profile to request. Default `query` when available, otherwise `readonly`. |
| `--wait` | Wait for the smoke task to finish. Default true. |
| `--timeout <seconds>` | Smoke task timeout in seconds. Default 60. |
| `--bidirectional` | Also ask the peer daemon to run a direct smoke task back to this machine. Requires `--wait=true`. |
| `--reverse-cwd <path>` | Cwd on this machine for the peer's reverse smoke task. Defaults to the current directory. |

Use `--bidirectional` when validating a two-machine setup: the first leg runs a direct smoke from this machine to the peer, and the reverse leg asks the peer's daemon to run its own smoke back, so timing reflects cmesh transport rather than agent planning latency.

```bash
cmesh peers test homelab
cmesh peers test homelab --bidirectional
cmesh peers test homelab --cwd ~/code/myapp
```

#### `cmesh peers bootstrap <ssh|tailscale> <target>`

Bootstrap cmesh on a remote machine over SSH or Tailscale SSH: install the binary, write a worker config, and install and start the OS service on the target.

```text
usage: cmesh peers bootstrap <ssh|tailscale> <target>
```

### `cmesh profile`

Inspect and manage permission profiles. These are normally driven by your agent through the MCP profile tools (see [MCP tools](mcp-tools.md)); the CLI subcommands exist for inspection and scripted setup.

```text
usage: cmesh profile templates|generate|validate|write|configure|current|use|reset
```

#### `cmesh profile templates`

List the built-in profile templates with name, risk level, enforcement confidence, and description.

| Flag | Description |
|---|---|
| `--json` | Print JSON. |

#### `cmesh profile generate [<template>]`

Render the TOML for a generated profile without writing config. Template defaults to `query`.

| Flag | Description |
|---|---|
| `--name <name>` | Generated profile name. |
| `--runner <name>` | Runner override for agent-backed profiles. |
| `--os <os>` | Shell policy OS filter: `windows`, `darwin`, or `linux`. |
| `--cap <capability>` | Capability override. Repeatable. |
| `--json` | Print JSON. |

#### `cmesh profile validate`

Validate one profile capability contract and reject unsafe contradictions (such as `writes=deny` with an edit capability). Prints `valid` on success. `--runner` is required.

| Flag | Description |
|---|---|
| `--name <name>` | Profile name. Default `custom`. |
| `--runner <name>` | Runner name. Required. |
| `--writes <mode>` | Write mode: `deny`, `repo_only`, or `allow`. |
| `--shell <mode>` | Shell mode: `deny`, `allowlisted`, or `allow`. |
| `--network` | Network access. |
| `--git-push` | Git push capability is enabled. |
| `--require-explicit` | Profile requires explicit selection. |
| `--confidence <level>` | Enforcement confidence. |
| `--cap <capability>` | Capability. Repeatable. |

#### `cmesh profile write [<template>]`

Generate a profile and append it to the config. Without `--confirmed` it prints the TOML preview and refuses to write. Template defaults to `query`.

| Flag | Description |
|---|---|
| `--config <path>` | Path to cmesh config. |
| `--name <name>` | Generated profile name. |
| `--runner <name>` | Runner override for agent-backed profiles. |
| `--os <os>` | Shell policy OS filter: `windows`, `darwin`, or `linux`. |
| `--confirmed` | Write the generated profile to the config. |
| `--force` | Allow appending even when the profile or runner name already appears in the config text. |
| `--cap <capability>` | Capability override. Repeatable. |

#### `cmesh profile configure [<template>]`

Add a built-in profile to the config, optionally allowing a single peer on it. Without `--confirmed` it previews. Template defaults to `repo-dev`.

| Flag | Description |
|---|---|
| `--config <path>` | Path to cmesh config. |
| `--name <name>` | Generated profile name. |
| `--runner <name>` | Runner override for agent-backed profiles. |
| `--peer <name>` | Peer name to allow on this profile. |
| `--confirmed` | Write the profile to the config. |
| `--force` | Append sections even when they already appear in the config. |
| `--json` | Print JSON. |

#### `cmesh profile current`

Show the default profile and the active per-peer profile context (scope, expiration, risk, elevation).

| Flag | Description |
|---|---|
| `--config <path>` | Path to cmesh config. |
| `--json` | Print JSON. |

#### `cmesh profile use <peer> <profile>`

Set the active profile context for a peer. For a profile with `require_lease = true`, `--ttl` is required and the command issues a worker lease over RPC.

```text
usage: cmesh profile use <peer> <profile> [--cwd path] [--ttl 30m]
```

| Flag | Description |
|---|---|
| `--config <path>` | Path to cmesh config. |
| `--cwd <path>` | Optional cwd scope note. |
| `--cwd-scope <mode>` | Cwd lease scope: `exact` or `recursive`. Default `exact`. |
| `--ttl <duration>` | Lease TTL such as `30m`. Required for profiles with `require_lease=true`. |

```bash
cmesh profile use homelab repo-dev --cwd ~/code/myapp --ttl 30m
```

#### `cmesh profile reset [<peer>]`

Clear the active profile context for one peer, or for all peers.

| Flag | Description |
|---|---|
| `--config <path>` | Path to cmesh config. |
| `--all` | Reset all active profiles to `query` and revoke owned worker leases. |

### `cmesh mcp`

Run the onboarding (setup) MCP server, or register it into a detected agent. On an interactive terminal without `--stdio`, it prints installer guidance instead of serving, so you do not accidentally tie up a TTY.

| Flag | Description |
|---|---|
| `--stdio` | Run the onboarding MCP server over stdio. Your agent launches this; you rarely run it by hand. |
| `--install` | Register the cmesh MCP into a detected agent (legacy; prefer `cmesh install`). |
| `--agent <name>` | Agent to install into with `--install`: `claude-code`, `codex`, or `all`. |

```bash
cmesh mcp --stdio
```

See [MCP tools](mcp-tools.md) for the tools this server exposes.

## Auxiliary commands

These are not part of the everyday surface. They exist for CI, scripting, low-level debugging, and the OS service manager. Some are superseded by the MCP wizard.

### `cmesh self-install`

Copy the running binary to a user PATH directory and ensure that directory is on PATH. Idempotent. Honors `$CMESH_INSTALL_DIR`; root installs to `/usr/local/bin`, other users to `~/.local/bin`. Takes no flags. `cmesh install` does this for you.

### `cmesh init`

Headless config generation, retained for CI and scripted setup. The interactive path is now `cmesh install` plus the MCP wizard. Without `--yes` it prompts for role, allowed cwds, peer names, allowed peers, and profiles, then writes and validates the config.

| Flag | Description |
|---|---|
| `--config <path>` | Path to cmesh config. |
| `--role <role>` | Machine role: `origin`, `worker`, or `both`. |
| `--origin` / `--worker` / `--both` | Shorthands that set the role. |
| `--allow-peer <id>` | Peer node/name allowed to delegate here. Repeatable. |
| `--allow-cwd <glob>` | Directory glob workers may use here. Repeatable. |
| `--peer <name>` | Peer to configure for outbound delegation. Repeatable. |
| `--profile <name>` | Profile to expose. Repeatable. |
| `--runner <name>` | Runner to use: `auto`, `claude-code`, or `codex`. Default `auto`. |
| `--yes` | Accept safe defaults without prompting. |
| `--force` | Overwrite an existing config. |
| `--connect` | Register the local MCP gateway after writing config. Default true. |

### `cmesh status`

Show recent tasks and the active per-peer profile summary.

| Flag | Description |
|---|---|
| `--config <path>` | Path to cmesh config. |
| `--json` | Print JSON. |

### `cmesh log <task_id>`

Print the worker log files for a task from the local data directory.

```text
usage: cmesh log <task_id>
```

| Flag | Description |
|---|---|
| `--config <path>` | Path to cmesh config. |

### `cmesh test <peer>`

Alias for `cmesh peers test <peer>`. Same flags and behavior.

### `cmesh hello`

Low-level debugging: call a peer's Hello RPC endpoint directly and print the JSON response. Normal flows go through the local gateway, not this command.

```text
usage: cmesh hello -url http://100.x.y.z:7778
```

| Flag | Description |
|---|---|
| `--url <url>` | Peer base URL, e.g. `http://100.x.y.z:7778`. Required. |

### `cmesh connect`

Superseded. Register the gateway from your agent via the cmesh MCP (`help me setup cmesh`) instead. Running it prints a notice, then attempts to register the gateway MCP into Claude Code and prints manual Codex instructions.

| Flag | Description |
|---|---|
| `--config <path>` | Path to cmesh config. |

### `cmesh version`

Print the cmesh version, commit hash, and build date.

| Flag | Description |
|---|---|
| `--json` | Print JSON. |

### `cmesh _service`

Internal. Runs the daemon as a service; invoked by the OS service manager, not by you. The leading underscore marks it as hidden.
