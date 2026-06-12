# cmesh

**Ask the coding agent on another machine to do work for you, without leaving your current agent session.**

cmesh lets Claude Code, Codex, or another local coding agent on your current machine delegate a task to a different machine you control.

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

In plain terms: **you set up the machines, Tailscale, and runner auth; cmesh handles the agent-to-agent handoff.**

## Why it exists

Coding agents are usually trapped on one machine, but real development work is not.

You may have a Linux box for production-like builds, a Mac for Apple targets, a Windows machine for native tests, a snapshot VM for risky edits, or a bare-metal server with the exact service state you need to inspect. Without cmesh, using those machines means manual SSH, copied prompts, lost context, fuzzy permissions, and no durable task state.

cmesh makes that handoff an agent tool call instead of a manual ritual.

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

cmesh is early, but it is structured as a real daemon and CLI, not a mockup. The first useful target is Mac/Linux peer delegation over Tailscale with static peers.

Implemented surfaces include `cmesh up`, `cmesh connect`, `cmesh status`, `cmesh log <task_id>`, `cmesh doctor tailscale`, a loopback `/mcp` endpoint, and the v1 mesh task API.

## Get started

1. Install and sign in to Tailscale on each machine.
2. Build cmesh with `go build -o cmesh ./cmd/cmesh`.
3. Copy an example config from [examples/config](examples/config).
4. Run `./cmesh doctor tailscale` on each node.
5. Start `./cmesh up --config ~/.config/cmesh/config.toml`.
6. Register the local MCP gateway with `./cmesh connect`.
7. Ask your agent to delegate a self-contained task.

Full setup instructions live in [docs/quickstart.md](docs/quickstart.md).

## Documentation

- [Architecture](docs/architecture.md)
- [Quickstart](docs/quickstart.md)
- [Configuration](docs/configuration.md)
- [Security model](docs/security.md)
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
