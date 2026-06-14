# cmesh

**Ask a machine you control to do bounded work through its local agent, without leaving your current agent session.**

## Experimental

cmesh is experimental remote task delegation for local AI agents.

It can cause real changes on real machines. In testing, cmesh has created files, run commands, opened desktop apps, and changed a Windows wallpaper from a Mac over a private tailnet.

Please do not use this on anything you love: machines, repos, credentials, or data you cannot afford to lose.

If you are down for testing early local-first agent infrastructure on trusted machines, welcome. Start with narrow peer allowlists, narrow cwd allowlists, and `readonly` profiles.

> This is not enterprise remote management software.
> This is an experimental agent delegation mesh for trusted private networks.
> Treat every worker profile as real authority on that machine.

cmesh lets Claude Code, Codex, or another local coding agent on your current machine delegate a scoped task to a different machine you control.

For example: you are on your Mac with Claude Code open, but you need to inspect a service, run a Linux build, or check logs on a bare-metal server in a datacenter. If both machines are on your Tailscale tailnet and the server has a supported runner installed, cmesh gives your local agent a tool for sending that task to the server and getting the result back.

```text
Use cmesh to ask the datacenter server to inspect /srv/myapp and check why the deploy failed.
```

Your local agent keeps running on your Mac. The work runs on the server.

## What this actually does

cmesh is for people who already have multiple machines and want their coding agents to use the right one for the job.

You bring:

- Tailscale on each machine.
- Claude Code, Codex, or another supported runner on each machine that should do work.
- Runner credentials/auth working locally on that machine.
- The repo, files, services, or toolchains available on that machine.

cmesh provides:

- a local MCP tool your agent can call;
- a daemon on each machine;
- secure peer-to-peer task dispatch over Tailscale;
- policy checks for which peers can run which tasks in which directories;
- durable task status and results;
- audit logs;
- git branch handoff for code changes.

In plain terms: **you set up the machines, Tailscale, and runner auth; cmesh handles policy-gated remote task execution through local agents.**

## Why it exists

Coding agents are usually trapped on one machine, but real development work is not.

You may have a Linux box for production-like builds, a Mac for Apple targets, a Windows machine for native tests, a snapshot VM for risky edits, or a bare-metal server with the exact service state you need to inspect. Without cmesh, using those machines means manual SSH, copied prompts, lost context, fuzzy permissions, and no durable task state.

cmesh makes that handoff an agent tool call instead of a manual ritual. It is task-driven rather than telemetry-driven: a human or agent asks a specific peer to do specific work, the peer evaluates policy, runs its local runner, and returns a structured, auditable result.

## How the handoff works

1. Your local agent calls the local cmesh MCP tool.
2. Your local cmesh daemon checks outbound policy.
3. It sends a task to the peer's cmesh daemon over Tailscale.
4. The peer daemon verifies the caller with Tailscale LocalAPI `whois`.
5. The peer daemon checks inbound policy.
6. The peer runs its local Claude Code/Codex runner.
7. Your local agent can poll status and fetch the result.

The remote MCP surface is never exposed. The only MCP endpoint is local loopback on the machine where the agent is running.

## Current status

cmesh is early, but it is structured as a real daemon and CLI, not a mockup. The first useful target is Mac, Windows, and Linux peer delegation over Tailscale with static peers.

The whole point is that you should rarely touch the CLI. The only real install is the cmesh binary plus the MCP registration; from there an agent-driven setup wizard does the rest. The small top-level command surface is:

- `cmesh install` — put the binary on PATH and register the cmesh MCP into your coding agents.
- `cmesh setup` — re-run the agent picker (for example, after installing a new agent).
- `cmesh up` — run the daemon, or manage it as a service (`cmesh up service install`).
- `cmesh doctor` — diagnostics for Tailscale, config, runners, and the local MCP gateway.
- `cmesh peers` — list and test peers.
- `cmesh uninstall` — mirror of the installer; remove the MCP and, separately consented, the rest.
- `cmesh mcp --stdio` — the onboarding MCP itself, launched by your agent (you do not run this by hand).

## Get started

Two things you do by hand. Everything else, you do by talking to your agent.

**1. Install the binary and register the MCP.**

```bash
curl -fsSL https://get.cmesh.dev | sh
```

Or build from this repo and run the installer:

```bash
go build -o cmesh ./cmd/cmesh
./cmesh install
```

On Windows, run the PowerShell installer from the repo root (it builds from
source or installs a prebuilt binary, then launches the picker):

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\install.ps1
```

(The `-ExecutionPolicy Bypass` is what lets an unsigned local script run on a
stock box; drop it if your policy already allows local scripts.)

`cmesh install` puts the binary on PATH and shows a short picker of the AI coding agents it detects (Claude Code, Codex). Choose where to register cmesh, confirm, and it upserts a stdio MCP entry that launches `cmesh mcp --stdio`. Do this on each machine.

**2. Open a new agent session and say:**

```text
help me setup cmesh
```

From there the cmesh MCP wizard drives everything: it detects Tailscale and peers (verify, don't ask), writes config after you confirm, installs and starts the daemon, registers the local gateway, and walks the two-machine handshake. There is no shared session between machines — each machine configures itself to trust and reach the other, and cmesh discovers the rest over the tailnet. To bring a second machine into the mesh, install cmesh there and, in a new session, say `continue helping me configure cmesh for this new node`.

Once a link is `ready`, ask your agent to delegate work:

```text
Use cmesh to ask homelab to run the test suite in ~/code/myapp (dev).
```

Full setup instructions, profiles, and example prompts live in [docs/quickstart.md](docs/quickstart.md).

## Uninstall

```bash
cmesh uninstall
```

`cmesh uninstall` mirrors the installer. It detects which agents have a cmesh MCP registered, shows only those, lets you choose which to remove, confirms, and removes the MCP entries. Because the only real install is the binary plus the MCP registration, that is the core action.

Anything beyond removing the MCP is separately consented — each is its own prompt, never bundled: stopping and removing the OS service, removing the config file, removing the data directory (database, audit logs, artifacts, worktrees — this deletes audit history), and removing the binary and PATH alias (only if cmesh installed them). The uninstaller is safe to run twice and never deletes anything cmesh did not create.

## Documentation

- [Architecture](docs/architecture.md)
- [Quickstart](docs/quickstart.md)
- [Configuration](docs/configuration.md)
- [Security model](docs/security.md)
- [Release process](docs/release.md)
- [Development](docs/development.md)
- [Delegation guidance](docs/delegation-guidance.md)
- [Runner manifests](docs/runner-manifests.md)
- [Tailscale grants](docs/tailscale-grants.md)

## Project shape

```text
cmd/cmesh                  CLI and daemon entrypoint
internal/                  daemon, policy, store, runner, and RPC packages
schemas/                   JSON Schemas
examples/                  runnable config and Tailscale grant examples
docs/                      architecture and operator guidance
```
