# cmesh

**Policy-governed delegation for coding agents across machines you control.**

cmesh turns your laptop, desktop, homelab box, VM, or Windows build target into a small local-first delegation mesh. An agent on one machine can hand a task to an agent on another machine, keep working, then retrieve a durable result with provenance, audit events, and a git branch to review.

The point is simple: coding agents are usually trapped on one machine, but real development work is not.

## Why it exists

You may have a Linux box for production-like builds, a Mac for Apple targets, a Windows machine for native tests, or a snapshot VM for risky edits. Without cmesh, using those machines means manual SSH, copied prompts, lost context, fuzzy permissions, and no durable task state.

cmesh makes that handoff a local tool call:

```text
delegate the Linux build to homelab and keep working
```

Your local agent talks only to the local cmesh gateway. The local daemon enforces outbound policy, sends a typed mesh RPC request over your tailnet, and the remote daemon enforces inbound policy before launching the configured runner.

## What it provides

- A loopback-only MCP gateway for agent harnesses.
- Daemon-to-daemon mesh RPC over Tailscale.
- Tailscale LocalAPI identity checks with cmesh policy authorization.
- Durable async task state in SQLite.
- Append-only JSONL audit events.
- Claude Code and Codex runner adapters.
- Deterministic git branch and commit handoff.
- Prompt-injection-aware result envelopes.

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

