# Quickstart

cmesh lets the coding agent on this machine ask the coding agent on another machine to do work.

> Experimental: cmesh can cause real changes on real machines. Do not use it on anything you love: machines, repos, credentials, or data you cannot afford to lose. Start on trusted private networks with narrow peer allowlists, narrow cwd allowlists, and `readonly` profiles.

The only thing you install by hand is the cmesh binary and the MCP registration. After that, you set up everything else by talking to your AI coding agent. The cmesh MCP is a stateless onboarding wizard: it detects rather than interrogates, confirms before it changes anything, and walks you all the way to a working two-machine mesh.

## 1. Install cmesh

Install the binary and register the cmesh MCP into your coding agent. Either path works.

One-line installer:

```bash
curl -fsSL https://get.cmesh.dev | sh
```

Or build from this repo and run the installer:

```bash
go build -o cmesh ./cmd/cmesh
./cmesh install
```

On Windows, run the PowerShell installer from the repo root. It mirrors the shell
installer — builds from source (or installs a prebuilt binary), puts `cmesh.exe`
on your PATH, and launches the agent picker:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\install.ps1
```

(`-ExecutionPolicy Bypass` lets an unsigned local script run on a stock box; drop
it if your policy already allows local scripts.)

`cmesh install` puts the binary on your PATH and then shows a short picker of the AI coding agents it detects (Claude Code, Codex). Choose where to register cmesh, confirm with **Y**, and it upserts a stdio MCP entry that launches `cmesh mcp --stdio`. That setup MCP works before any daemon or config exists.

To run the agent picker again later (for example, after installing a new agent), use:

```bash
cmesh setup
```

Do this on every machine that should take part in the mesh.

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

First, install cmesh on the second machine exactly as in step 1 (installer or build + `cmesh install`).

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

## 4. After setup: profiles and example prompts

When a link reaches `ready`, the wizard prints the per-peer default profile and a few ready-to-use prompts.

### Profiles

Each peer exposes a set of profiles. The default for delegated work is `dev`. You select a different profile per task by naming it in your request:

```text
Use cmesh to ask homelab to report disk usage for / (readonly).
Use cmesh to ask homelab to run the test suite in ~/code/myapp (dev).
```

From an agent delegate call you pass it explicitly, for example `profile: "readonly"`.

The profile menu, from least to most authority:

- `readonly` — read files only.
- `dev` — read, edit a repo, and run repo commands. This is the default.
- `network` — adds outbound network access.
- `interactive` — adds interactive runner capability.
- `git_push` — can push commits.

> Security note: `readonly` and `dev` are the safe defaults. Anything past `dev` (`network`, `interactive`, `git_push`) is real authority on the worker machine. Grant it only to peers you trust, and scope it to that peer.

### Example prompts

```text
Use cmesh to ask homelab to summarize ~/code/myapp.
Use cmesh to ask homelab to check git status in ~/code/myapp (readonly).
Use cmesh to ask homelab to run the native build in ~/builds/app (dev).
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

The wizard handles first-run setup. A few commands are useful afterward for diagnostics and direct operation.

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

## Notes for Windows workers

On Windows, the service should run under the same account that has Tailscale and runner auth so Claude Code or Codex reuse that user's credentials. When configuring a Windows worker, keep runner commands absolute, for example:

```toml
[runners.claude-code]
command = ["C:\\Users\\scott\\AppData\\Roaming\\npm\\claude.cmd", "-p"]
```

A service installed without an explicit user runs as LocalSystem and will not inherit the interactive user's runner auth or PATH.
