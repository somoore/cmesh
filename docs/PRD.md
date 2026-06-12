# cmesh Product Requirements

Status: Draft v1
Date: 2026-06-12

## Thesis

cmesh is a local-first delegation plane for coding agents. It gives a local agent one MCP tool surface for delegating work to peer machines on the user's tailnet, while daemon-to-daemon execution, identity, policy, durable task state, and audit remain outside the model's control.

The core primitive is simple: an agent asks the local cmesh daemon to delegate a task, the local daemon applies outbound policy, the remote daemon applies inbound policy, a runner executes the task under a declared profile, and the origin receives a bounded result envelope that treats worker output as claims.

## Product Goals

1. An agent on one machine can delegate a self-contained coding task to another machine with one tool call.
2. The product is harness-agnostic. MCP is only the local orchestrator surface; worker execution is behind a Runner interface.
3. Setup on a machine takes no more than three commands after Tailscale is running.
4. No public internet ports are opened. Mesh RPC binds only to the Tailscale IP or a tsnet listener.
5. Task state is durable across MCP disconnects, harness restarts, and daemon restarts.
6. Every task has provenance: origin node, origin session, harness versions, prompt hash, delegation chain, hop count, requested profile, effective profile, and runner controls used.
7. Every dispatch, denial, result, cancellation, and failure is audit logged on the relevant daemons.
8. Runner profiles make evidence-backed enforcement claims. They declare exact controls, confidence, known gaps, credential source, and billing route.
9. Worker results are treated as prompt-injection-bearing content and never as authority.

## Non-Goals for v1

- Remote MCP.
- Cloud-hosted workers.
- Cross-organization federation.
- Interactive permission prompt proxying.
- A global scheduler.
- Implicit file transfer over the mesh.
- A web dashboard.
- Multi-tenant control plane.

## Target User

The first user is a developer with two to five personal machines already using terminal coding agents. Typical nodes include a laptop, a desktop, a Linux build box, a Windows test target, and a snapshot-protected VM for risky tasks.

The product should feel like a local tool, not infrastructure. A normal workflow is:

```text
delegate the Linux build to homelab and keep working
```

## Architecture

cmesh has two deliberately separate protocols:

1. Local MCP gateway: harness to local daemon, loopback only.
2. Mesh RPC: daemon to daemon, HTTP+JSON over the tailnet.

The orchestrating agent never connects to a remote machine. Its local daemon connects to the remote daemon, and both daemons enforce policy. The origin daemon enforces outbound delegation policy. The worker daemon enforces inbound authorization, cwd policy, runner availability, and profile ceilings.

There is no broker, shared database, SaaS control plane, or central scheduler. Every daemon owns its local config, task store, runner manifests, and audit log.

## Transport and Identity

Tailscale is the v1 default transport.

Required behavior:

- Mesh RPC binds only to the node's Tailscale IP or a tsnet listener.
- Mesh RPC never binds to 0.0.0.0.
- Non-tailnet source addresses fail closed before policy evaluation.
- In Tailscale host mode, the daemon uses `tailscale.com/client/local` to query LocalAPI.
- Daemon-to-daemon identity in Tailscale mode comes from LocalAPI `whois`, not cmesh bearer tokens.
- Tailscale tags and grants are defense in depth, not the trust boundary.

The trust boundary is cmesh task authorization. A whois-verified peer can still be denied by cmesh policy.

## Policy Model

Policy is evaluated in layers:

1. Network admission through Tailscale grants.
2. Connection identity through LocalAPI whois.
3. Task authorization through cmesh policy.
4. Runner execution under the effective profile.

Task authorization considers:

- allowed peers;
- allowed working directories;
- runner availability;
- requested profile;
- effective profile ceiling;
- network permission;
- git push permission;
- outbound origin policy;
- hop limit and cycle detection.

`cwd` is always normalized before policy matching. Implementations must resolve symlinks, `..`, case-insensitive aliases, drive-letter aliases, platform separators, and home expansion before comparing against `allowed_cwds`.

## Local MCP Tool Surface

The local gateway exposes these model-facing tools:

| Tool | Behavior |
| --- | --- |
| `list_peers()` | Lists live mesh nodes, OS, runners, versions, capability tags, load, and reachability. |
| `peer_info(peer)` | Returns runner manifests, allowed profiles, allowed cwd summary, queue state, and toolchain summary. |
| `delegate(peer, prompt, cwd, runner?, profile?, timeout?, wait?, allow_dirty?)` | Dispatches a task and returns `task_id` by default. With `wait=true`, waits up to the timeout for short tasks. |
| `task_status(task_id)` | Returns queued, running, done, failed, denied, or cancelled with elapsed time and last activity. |
| `task_result(task_id)` | Returns the bounded canonical result envelope. |
| `cancel(task_id)` | Cancels a queued or running task and kills the worker process group when possible. |

Local MCP hardening requirements:

- bind only to loopback;
- validate Host and Origin;
- reject non-loopback values unless explicitly configured for local testing;
- ignore forwarded identity headers;
- never expose this surface through Tailscale Serve, Funnel, cloudflared, nginx, or another proxy.

## Mesh RPC

Mesh RPC is daemon-to-daemon HTTP+JSON. Phase 1 endpoints:

```text
GET  /v1/hello
POST /v1/tasks
GET  /v1/tasks/{task_id}
GET  /v1/tasks/{task_id}/result
POST /v1/tasks/{task_id}/cancel
```

Every request uses a schema-version header. Every mutating request is audited.

`GET /v1/hello` exists in Phase 1 for static-peer validation, version negotiation, health checks, and future discovery.

Example response:

```json
{
  "node_id": "node-01HZABC123",
  "node_name": "homelab-vm",
  "schema_versions": ["v1"],
  "runners": ["claude-code"],
  "capabilities": ["read_files", "edit_repo", "execute_repo_commands"],
  "server_time": "2026-06-12T00:00:00Z"
}
```

## Task Lifecycle

Worker durable state:

```text
queued -> running -> done
queued -> running -> failed
queued -> denied
queued -> cancelled
running -> cancelled
```

Once `delegate` returns a `task_id`, task lookup survives:

- MCP disconnect;
- harness restart;
- origin daemon restart;
- worker daemon restart, with fail-closed terminal state for interrupted worker tasks.

Worker restart semantics in v1 are fail-closed. On startup, the daemon detects interrupted running tasks, kills known process groups when possible, and marks affected tasks `failed` with `worker_daemon_restarted`.

## Idempotency

Idempotency is part of the security model.

The idempotency key is:

```text
origin_node_id + origin_session_id + task_id
```

The replay payload hash is SHA-256 of the raw validated request body.

Rules:

- same key and same payload hash returns the existing task;
- same key and different payload hash returns `409 task_conflict`;
- same task ID from a different origin identity is rejected;
- completed-task idempotency records are retained for 30 days by default.

## Runners and Manifests

A Runner adapter is responsible for:

1. headless subprocess invocation;
2. profile translation into native harness controls;
3. result normalization into the cmesh result schema.

Runner profiles are evidence, not intent. Each profile declares:

- capability claims;
- native controls or flags;
- enforcement confidence;
- known gaps;
- credential source;
- billing route;
- bare or non-bare behavior;
- network and git push posture.

v1 ships the Claude Code runner first. Codex and other harnesses are additional runners, not architectural changes.

Bare mode is profile-controlled. A runner must never silently fall back from bare to non-bare. If a bare profile lacks required credentials, it returns `profile_unavailable`.

## Git Contract

Artifacts move through git, not the mesh.

Worker behavior:

- refuse dirty worktrees unless `allow_dirty=true`;
- create deterministic branches named `cmesh/<task-id>/<slug>`;
- commit locally by default;
- push only when the effective profile permits both network and git_push;
- include cmesh metadata footers in commit messages.

Commit footers:

```text
cmesh-task: <task-id>
cmesh-origin: <origin-node>
cmesh-worker: <worker-node>
cmesh-profile: <profile>
```

No git repo means the task may still run, but changed files are reported as local artifacts and are not transferred.

## Result Contract

`task_result` returns the canonical result envelope defined by `schemas/result.v1.json`.

Key properties:

- worker prose is confined to `worker_summary_untrusted`;
- full transcripts are not auto-inlined;
- `tests.claimed_passed` is a claim, not a fact;
- `verification_required` tells the origin to verify before relying on the result;
- `recommended_origin_checks` gives concrete local checks.

The origin guidance treats results as claims and verifies high-risk work locally.

## Error Codes

Stable v1 codes:

```text
peer_unreachable
peer_unauthorized
runner_unavailable
profile_unavailable
profile_denied
cwd_denied
dirty_worktree
version_mismatch
task_conflict
timeout
needs_human
worker_failed
worker_daemon_restarted
cancelled
result_too_large
transcript_unavailable
```

Existing codes do not change meaning. New codes may be added in later versions.

## Human Surface

v1 provides:

```bash
cmesh status
cmesh log <task_id>
cmesh doctor
```

The CLI exposes peer state, running tasks, recent results, setup diagnostics, and human-readable transcripts. Transcript access is CLI-first in v1.

## Success Metrics

- Fresh machine to first successful delegation in under five minutes after Tailscale is running.
- At least five useful real delegations per week after week two.
- Zero tasks executed outside cmesh policy.
- 100 percent of dispatches appear in origin and worker audit logs.
- Zero cmesh-stored daemon-to-daemon credentials in Tailscale mode.
- Zero enforcement claims above manifest confidence.
- Restart tests produce durable, queryable terminal states.
- Shipped guidance causes agents to delegate appropriately without human coaching.
