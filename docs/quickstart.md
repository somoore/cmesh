# Quickstart

cmesh lets the coding agent on this machine ask the coding agent on another machine to do work.

> Experimental: cmesh can cause real changes on real machines. Do not use it on anything you love: machines, repos, credentials, or data you cannot afford to lose. Start on trusted private networks with narrow peer allowlists, narrow cwd allowlists, and the `query` profile.

The only thing you install by hand is the cmesh binary and the MCP registration. After that, you set up everything else by talking to your AI coding agent. The cmesh MCP is a stateless onboarding wizard: it detects rather than interrogates, confirms before it changes anything, and walks you all the way to a working two-machine mesh.

This guide is the same on macOS, Linux, and Windows. Each install path below is complete on its own — pick your OS and copy-paste; you never have to translate steps from another platform.

> **What success looks like (the 30-second mental model).** cmesh needs **two** machines you control, both on your Tailscale tailnet, each with a coding agent (Claude Code or Codex) already working locally. You install cmesh on both, then on each say "help me setup cmesh" and answer a few yes/no prompts. Each machine configures itself to trust and reach the other. When the link between them reports `ready`, you are done: from your local agent you can now delegate a scoped task to the other machine and get the result back in your session. Everything below is just getting two machines to `ready`.

New to the model? [Concepts](concepts.md) explains peers, profiles, and the trust boundary in plain terms. For the full command and tool surface, see [Commands](commands.md) and [MCP tools](mcp-tools.md). The [README](../README.md) is the short pitch.

## Prerequisites

These apply to **every** machine that will take part in the mesh, on every OS:

- **Tailscale installed and logged in.** Both machines must be on the same tailnet and able to see each other. Verify with `tailscale status`.
- **A supported runner already working locally** — Claude Code or Codex — invokable from your shell and authenticated. cmesh delegates work *through* your existing agent; it does not install or authenticate one for you. Confirm the runner works on its own before exposing it through cmesh.
- **A Go toolchain only if building from source.** The default install paths below use pre-built release binaries. You need Go only for the source-build fallback.

You do not need to install a daemon, write any config, or open any ports by hand. The wizard does all of that, with your confirmation.

## 1. Install cmesh

Installing means putting the `cmesh` binary on your PATH and registering the cmesh MCP into your coding agent. Do it on **every** machine that should take part in the mesh.

The installer detects your OS and architecture, then downloads the matching GitHub release asset:

- `darwin/amd64` -> `cmesh_darwin_amd64.tar.gz`
- `darwin/arm64` -> `cmesh_darwin_arm64.tar.gz`
- `linux/amd64` -> `cmesh_linux_amd64.tar.gz`
- `linux/arm64` -> `cmesh_linux_arm64.tar.gz`
- `windows/amd64` -> `cmesh_windows_amd64.tar.gz`
- `windows/arm64` -> `cmesh_windows_arm64.tar.gz`

### macOS

```bash
curl -fsSL https://raw.githubusercontent.com/somoore/cmesh/main/scripts/install.sh | sh
```

The installer puts `cmesh` on your PATH, then shows a short picker of the AI coding agents it detects (Claude Code, Codex). Choose where to register cmesh, confirm with **Y**, and it upserts a stdio MCP entry that launches `cmesh mcp --stdio`. That setup MCP works before any daemon or config exists.

To install a specific release instead of the newest published release:

```bash
curl -fsSL https://raw.githubusercontent.com/somoore/cmesh/main/scripts/install.sh | CMESH_VERSION=v0.1.0-prealpha.2 sh
```

### Linux

```bash
curl -fsSL https://raw.githubusercontent.com/somoore/cmesh/main/scripts/install.sh | sh
```

The installer puts `cmesh` on your PATH, then shows a short picker of the AI coding agents it detects (Claude Code, Codex). Choose where to register cmesh, confirm with **Y**, and it upserts a stdio MCP entry that launches `cmesh mcp --stdio`. That setup MCP works before any daemon or config exists.

To install a specific release instead of the newest published release:

```bash
curl -fsSL https://raw.githubusercontent.com/somoore/cmesh/main/scripts/install.sh | CMESH_VERSION=v0.1.0-prealpha.2 sh
```

### Windows

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -Command "irm https://raw.githubusercontent.com/somoore/cmesh/main/scripts/install.ps1 | iex"
```

`-ExecutionPolicy Bypass` lets an unsigned downloaded script run on a stock box; drop it if your policy already allows it. The installer downloads the matching `cmesh_windows_<arch>.tar.gz` release, puts `cmesh.exe` on your user PATH, prints the installed version, and then launches the same agent picker described above. Choose where to register cmesh, confirm with **Y**, and it upserts the stdio MCP entry that launches `cmesh mcp --stdio`.

To install a specific release instead of the newest published release:

```powershell
$env:CMESH_VERSION = "v0.1.0-prealpha.2"; powershell -NoProfile -ExecutionPolicy Bypass -Command "irm https://raw.githubusercontent.com/somoore/cmesh/main/scripts/install.ps1 | iex"
```

If you added cmesh to PATH for the first time, **open a new terminal** so the PATH change takes effect before running `cmesh` by name.

> Planning to run this Windows machine as a **worker** (a machine that *receives* delegated tasks)? Note now that the cmesh service should later run under the same account that holds your Tailscale and runner auth, and that runner command paths in config must be absolute. The details are in [step 3](#3-add-a-second-machine-the-symmetric-handshake) and [Configuration](configuration.md#runners); you do not need them yet to finish installing.

### Re-running the picker

To run the agent picker again later (for example, after installing a new agent), use:

```bash
cmesh setup
```

`cmesh setup` is an alias for the picker `cmesh install` shows; it does not reinstall the binary.

### Building from source

If you are working from a checkout and want to build locally instead of using a release archive, run this from the repo root:

```bash
go build -o cmesh ./cmd/cmesh
./cmesh install
```

On Windows from a checkout:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\install.ps1
```

## 2. Talk to the wizard

Open a **new agent session** (MCP servers load at session start) and say:

```text
help me setup cmesh
```

That is the whole interface. The cmesh MCP wizard takes it from there. It re-derives where you are in setup on every step, so it is safe to stop and resume, and safe to run again.

What the wizard does, in order, on this first machine:

1. **Detects Tailscale and peers.** It verifies that Tailscale is up and how many tailnet peers are online. It does not ask you whether Tailscale is installed; it checks and tells you. If Tailscale is down, it guides you to start or log in.
2. **Writes config (confirmed).** It auto-fills your role, safe working directories, and any cmesh-capable peers it found, shows you the result, and writes `~/.config/cmesh/config.toml` only after you confirm.
3. **Installs and starts the daemon (confirmed).** It installs the OS service (launchd on macOS, a systemd user service on Linux, a Windows service on Windows), starts it, and polls the local gateway `/health` until it is up. You do not run a daemon command by hand.
4. **Registers the gateway.** Once the daemon is healthy, it swaps your agent's cmesh MCP from the stdio setup server to the local HTTP gateway used for delegation. This is the one point where you may be asked to restart your agent session.

Throughout, the wizard only stops to ask for **consent** to change something (write config, install or start the service, trust a peer). It never asks you to confirm facts it can detect for itself.

Under the hood these steps are cmesh MCP tools (`onboarding_status`, `configure_node`, `start_service`, `link_peer`, `register_gateway`). You normally never call them directly, but [MCP onboarding tools](mcp-onboarding.md) documents each one if you want to follow along or drive a step by hand.

> Codex note: registering the gateway MCP varies by Codex CLI version. If the wizard cannot do it automatically, it prints the exact manual steps instead of failing. Follow those, then continue.

## 3. Add a second machine (the symmetric handshake)

A single machine cannot delegate anywhere yet. To form a mesh you set up a second machine. There is **no shared session between machines**: the second machine does not resume the first machine's session or pick up where it left off. Instead, **each machine configures itself to trust and reach the other**, and cmesh discovers the rest over the tailnet.

First, install cmesh on the second machine exactly as in [step 1](#1-install-cmesh) — same OS path, same `cmesh install`. The two machines do not have to share an operating system; a Mac and a Windows box pair fine.

Then, on the **second machine**, open a new agent session and say:

```text
continue helping me configure cmesh for this new node
```

This re-enters the same stateless wizard on the new machine. "Continue" here means "continue setting up *the mesh*," not "resume the other machine's session." On the new machine the wizard:

1. Detects that this is a fresh node and sees the first machine online on the tailnet.
2. Writes this machine's config, recording the first machine in its inbound `allowed_peers` by its **whois-matchable identity** — the Tailscale DNS hostname label (e.g. `scotts-macbook-pro`) and stable node ID, never the human display name (e.g. "Scott's MacBook Pro"). This is the identity Tailscale `whois` reports for an incoming connection, so it is what the inbound policy can actually match. It confirms with you before writing.
3. Installs and starts this machine's daemon so it is reachable on the tailnet.

Once the second machine's daemon is up and reachable, finish on the **first machine**. Its wizard (or a fresh `help me setup cmesh`) now discovers the new machine, writes the reciprocal peer entry and outbound trust, and runs a **bidirectional smoke test** in both directions. When both directions pass, both machines report `ready`.

The ordering matters: the new machine has to configure itself and come up on the tailnet **before** the first machine's smoke test can succeed. The wizard enforces this for you.

### A note on Windows workers

If the machine receiving delegated work is Windows, the cmesh service should run under the **same account** that has Tailscale and runner auth, so Claude Code or Codex reuse that user's credentials. A service installed without an explicit user runs as **LocalSystem** and will not inherit the interactive user's runner auth or PATH. Keep runner commands **absolute** in config, for example:

```toml
[runners.claude-code]
command = ["C:\\Users\\scott\\AppData\\Roaming\\npm\\claude.cmd", "-p"]
```

[Configuration](configuration.md#runners) covers the service-account and runner-path details (including installing the service under a named user with `cmesh up service install --user`); [Security](security.md) covers why a Windows worker account is a standing remote-execution credential to keep narrow.

## 4. After setup: profiles and example prompts

When a link reaches `ready`, the wizard prints the per-peer default profile and a few ready-to-use prompts.

### Profiles

Each peer exposes a set of profiles. The generated default is `query`: deterministic cmesh probes with no shell, no writes, and no arbitrary agent execution. You select a different profile per task by naming it in your request:

```text
Use cmesh to ask homelab to report disk usage for / (query).
Use cmesh to ask homelab to run the test suite in ~/code/myapp (repo-dev).
```

From an agent delegate call you pass it explicitly, for example `profile: "query"` or `profile: "repo-dev"`.

The profile menu, from least to most authority:

- `query` - deterministic cmesh probes, no shell, no writes, no arbitrary agent execution. This is the default.
- `query-network` - deterministic probes plus bounded typed network checks.
- `query-shell` - allowlisted read-oriented diagnostic commands, no writes.
- `readonly-agent` / `readonly` - agent-assisted open-ended read-only system and file inspection with shell denied by default; prefer `query` when a typed probe is enough.
- `repo-dev` / `dev` - agent edits/tests inside allowed cwd, explicit and lease-gated.
- `desktop-control`, `system-change`, `network`, `interactive`, `git_push` - elevated profiles for desktop/session, system, network, or push authority.

> Security note: `query` is the safe default. Agent-backed and elevated profiles are real authority on the worker machine. Grant them only to peers you trust, and scope them to that peer. See [Security](security.md) for the full model and [Configuration](configuration.md) for how to scope a profile to a single origin.

### Example prompts

```text
Use cmesh to ask homelab to summarize ~/code/myapp.
Use cmesh to ask homelab to check git status in ~/code/myapp (query).
Use cmesh to ask homelab to run the native build in ~/builds/app (repo-dev).
```

Your local agent keeps running on your machine. The work runs on the peer, and the result comes back to your session.

## Uninstall

To remove cmesh, run:

```bash
cmesh uninstall
```

`cmesh uninstall` mirrors the installer. It detects which agents currently have a cmesh MCP registered, shows only those, lets you multi-select which to remove, confirms with **Y**, and removes the MCP entries.

Because the only real install is the binary plus the MCP registration, removing the MCP is the core action. Anything else is **separately consented** — each step is its own Y/N prompt, never bundled:

- Stop and remove the OS service.
- Remove the config file (`~/.config/cmesh/config.toml`).
- Remove the data directory (`~/.local/share/cmesh`: database, audit logs, artifacts, worktrees) — this deletes audit history.
- Remove the binary and PATH alias (only if cmesh installed them).

The uninstaller is safe to run twice and never deletes anything cmesh did not create. It finishes with an honest report of what was removed and what was left.

## Checking and operating a node

The wizard handles first-run setup. A few commands are useful afterward for diagnostics and direct operation. (The full command surface is in [Commands](commands.md).)

Verify a node:

```bash
cmesh doctor
```

It checks config, Tailscale, LocalAPI, tailnet binding, runner availability, and the local MCP endpoint. For runner-specific or service-context detail:

```bash
cmesh doctor runner claude-code
cmesh doctor service-context
```

List and test peers:

```bash
cmesh peers
cmesh peers test homelab
cmesh peers test homelab --bidirectional
cmesh peers test homelab --cwd ~/code/myapp   # target a specific directory
```

The smoke test creates a real readonly task, fetches the result envelope, and verifies the expected raw smoke response and audit event — a runner that exits successfully without actually doing the task does not count as healthy. With no `--cwd`, the **worker** picks a directory its own `allowed_cwds` permits, so the smoke works across machines (and across operating systems) without you knowing a path that exists on the other side; pass `--cwd` only to target a specific directory. Use `--bidirectional` when validating a two-machine setup: the first leg runs a direct smoke task from this machine to the peer, and the reverse leg asks the peer's daemon to run its own smoke task back, so timing reflects cmesh transport rather than agent planning latency.

### Fixing a broken or stale peer entry

If a peer stops authenticating (`peer_unauthorized`), or a `[peers.*]` entry looks wrong, you do **not** edit config by hand. Tell your agent:

```text
A cmesh peer entry looks wrong. Diagnose and fix my peers.
```

The agent calls cmesh's `repair_peers` tool. It checks every configured peer against its authoritative Tailscale `whois` identity, returns a per-peer diff (which entries are `ok`, `fixable`, in `conflict`, or `unreachable`), and on your confirmation rewrites the mismatched entries to the correct identity, removes stale duplicate entries, and reconciles the allowlists. It preserves your existing trust posture (it never grants a peer more access than it already had) and leaves conflicts and unreachable peers untouched for you to review. When the agent is on the local gateway, the repaired config takes effect after the daemon restarts; the tool says so.

The daemon normally runs as the service the wizard installed. To run it in the foreground for debugging:

```bash
cmesh up
```

Startup runs a runner preflight before advertising the mesh listener. Use `cmesh up --skip-preflight` only for debugging a broken node locally.

The local MCP gateway used for delegation is:

```text
http://127.0.0.1:7777/mcp
```

Direct mesh RPC calls such as `POST http://100.x.y.z:7778/v1/tasks` are for low-level debugging only. Normal flows go through the local gateway so outbound policy and task provenance are applied before the worker sees the request.

## Where to go next

- [Concepts](concepts.md) — peers, profiles, and the trust boundary explained.
- [Commands](commands.md) — the full `cmesh` command surface.
- [MCP tools](mcp-tools.md) — the tools your agent calls (`delegate`, `task_status`, `repair_peers`, and more).
- [MCP onboarding tools](mcp-onboarding.md) — the wizard's tools, if you want to drive setup by hand.
- [Configuration](configuration.md) — config file reference, including Windows workers and profile scoping.
- [Security](security.md) — the defense-in-depth model and recommended hardening.
- [README](../README.md) — the short overview of what cmesh is and why.
