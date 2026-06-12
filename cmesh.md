# cmesh — Peer Agent-to-Agent Delegation Across Machines

**Status:** Draft v0.5  
**Author:** Scott Moore  
**Date:** 2026-06-12  
**Working name:** `cmesh`  
**Naming constraint:** final name must be vendor-neutral; no harness or model-provider trademarks. Current shortlist: `gofer`, `legate`, `skein`, `holler`, `trunkline`.

## Thesis

*cmesh is a local-first **delegation plane** for coding agents: one local MCP tool surface, daemon-to-daemon execution over your tailnet, policy-bound runners, durable async task state, and auditable provenance for every cross-machine handoff.*

## Revision notes

v0.5 incorporates the final implementation clarifications:

- daemon service account must be able to access Tailscale LocalAPI;
- mesh RPC binds only to the Tailscale IP or tsnet listener;
- LocalAPI uses `tailscale.com/client/local`, not the deprecated control-plane client;
- daemon-to-daemon identity in Tailscale mode comes from `whois`, not bearer tokens;
- idempotency/replay behavior is explicit;
- worker results are a prompt-injection boundary;
- transcript access is human/CLI-first in v1;
- path allowlists require realpath/case/alias normalization;
- git commits include cmesh task metadata;
- `/v1/hello` exists in Phase 1 even before dynamic discovery;
- restart semantics are fail-closed in v1.

---

## 1. Summary

cmesh makes every machine you own a peer in a delegation mesh. Any coding-agent session on any machine can hand a task to an agent on another machine, await the result asynchronously, and get back structured output with full provenance — using one daemon per machine and one local MCP registration per harness.

The architecture is **two deliberately separate protocols**:

1. **Local MCP surface** — harness ↔ local cmesh daemon, loopback only. This is the only MCP surface in the system.
2. **Mesh RPC surface** — daemon ↔ daemon over the tailnet: typed, versioned, idempotent HTTP+JSON, policy-heavy by design.

The orchestrating agent never talks to a remote machine directly. Its local daemon does, and both daemons enforce policy:

- the origin daemon enforces **outbound delegation policy**;
- the worker daemon enforces **inbound task authorization** and runner/profile policy.

The product is **harness-agnostic by design**: MCP on the orchestrator side, and a pluggable Runner interface on the worker side. v1 ships Claude Code as the first runner; Codex CLI and other harnesses are additional runners, not architectural events.

The core bet: **delegation should be a tool call, not infrastructure.** Everything hard — transport, identity, authorization, audit, loop prevention, restart behavior, result contracts — lives in the daemons so the agent-facing surface stays a clean primitive.

---

## 2. Problem

Coding-agent harnesses such as Claude Code, Codex CLI, and peers are primarily single-machine by design. Existing projects solve adjacent problems:

- **Human-as-router:** a person orchestrates multiple agents from a phone or remote UI.
- **Single-host orchestration:** multiple agents coordinate, but all sessions share one machine.
- **Room-based coordination:** cross-machine activity is mediated through a workspace backplane and client app.

**cmesh focuses specifically on agent-initiated, policy-governed, peer-to-peer delegation across a user's own machines.**

That combination — agent-initiated, policy-governed, provenance-rich, no center — is the differentiated primitive, regardless of what prior art exists for any single ingredient.

The gap matters because real workloads are machine-heterogeneous:

- macOS-only builds;
- Windows-only test targets;
- a beefy Linux box for long compiles;
- a snapshot-protected VM for risky operations;
- machines with unique toolchains, SDKs, credentials, or hardware.

---

## 3. Goals and Non-Goals

### Goals

1. An agent on machine A can delegate a task to an agent on machine B with one tool call.
2. **Harness-agnostic core:** nothing harness-specific above the Runner interface or below the local MCP surface. v1 implements one runner, Claude Code; adding a second runner, such as Codex CLI, must require zero changes outside the runner package and node config.
3. Setup per machine is no more than three commands and no more than five minutes.
4. Zero open ports on the public internet. **Transport encryption and peer identity come from Tailscale; task authorization is always a daemon policy decision.** Tailscale tells cmesh who connected; cmesh decides whether that peer may delegate this task, in this cwd, with this runner, under this profile.
5. Async-first and **durable:** task lifecycle is independent of MCP session lifecycle. Once `delegate` returns a `task_id`, state persists in SQLite and survives harness disconnect, MCP reconnect, and daemon restart. There is no correctness dependency on MCP transport resumability.
6. Every task carries provenance: origin node, origin session, harness and version on both ends, prompt hash, hop count, delegation chain.
7. Every dispatch, denial, result, cancellation, and failure lands in append-only audit logs on both relevant daemons.
8. Safe by default: workers run under explicit, manifest-backed permission profiles; the harness's skip-all-permissions escape hatch is never required and never the documented path.
9. **Enforcement claims are evidence, not intent:** every runner profile declares exact native controls, claimed capabilities, confidence, known gaps, and billing/credential route.

### Non-Goals for v1

- Mobile or human remote-control UI.
- Cloud-hosted workers.
- Cross-org federation or multi-tenant control plane.
- Proxying interactive permission prompts back to the origin session.
- A fair scheduler. v1 is dispatch plus small per-node concurrency caps.
- Remote MCP. The mesh never exposes an MCP server beyond loopback.
- Implicit file transfer. Code/artifacts move through git unless a future explicit capability-gated transfer feature is added.

---

## 4. Users

### Primary user

A single developer with two to five personal machines who already lives in terminal coding agents. Example machines:

- laptop;
- desktop;
- homelab VM;
- Windows build/test target;
- snapshot-protected experimental VM.

### Secondary users, later

Small teams sharing a tailnet with per-user grants and mixed harnesses across machines.

---

## 5. Transport: Tailscale

| | Tailscale | Raw WireGuard | cloudflared |
|---|---|---|---|
| Setup per node | `tailscale up`, done | Manual keys, IPs, configs per node pair | Tunnel tokens, DNS, Cloudflare account |
| Topology | Mesh, direct p2p with DERP fallback | Mesh, fully manual | Hub-and-spoke through Cloudflare edge |
| Identity primitive | LocalAPI `whois`: every inbound connection resolves to authenticated node + user/tag | None; IP is not identity | Cloudflare Access JWTs |
| Policy | Grants preferred; legacy ACLs supported; tag-based; centrally managed | DIY firewall | Access policies |
| Third party in data path | No for direct p2p; DERP relays see encrypted packets | No | Yes |

### Decision

Tailscale is the default and documented happy path.

Reasons:

1. **Peer identity:** LocalAPI `whois` gives the daemon authenticated node/user identity for inbound connections.
2. **No daemon-to-daemon bearer-token subsystem in Tailscale mode:** whois removes the need to mint, rotate, store, or leak cmesh-specific RPC tokens between tailnet peers.
3. **Policy as defense in depth:** grants can target `tag:cmesh` devices on the mesh RPC port.
4. **Simple setup:** the target machines already run Tailscale.
5. **Future tsnet path:** embedded tsnet can later make the daemon its own tagged tailnet node.

Manual mode using raw WireGuard remains possible through static peer config plus shared-secret auth, but it is not optimized for v1. cloudflared is rejected for the core product because it introduces a third party into the data path between machines you own and is shaped around public service exposure. It may be useful later for webhook ingress.

### Reference Tailscale grant

```json
{
  "grants": [
    {
      "src": ["tag:cmesh"],
      "dst": ["tag:cmesh"],
      "ip": ["tcp:7778"]
    }
  ]
}
```

**Required warning in docs:** tags and grants are not the trust boundary. Daemon policy still applies to every request the grant admits.

---

## 6. Architecture

```text
┌────────────────────────────────── tailnet ──────────────────────────────────┐
│                                                                              │
│  mac-studio                                          homelab-vm              │
│  ┌────────────────────────┐                          ┌────────────────────┐  │
│  │ agent harness          │                          │                    │  │
│  │ Claude Code, Codex…    │                          │   cmesh daemon     │  │
│  │      │ MCP loopback    │      mesh RPC            │   ┌─────────────┐  │  │
│  │      ▼ :7777           │   HTTP+JSON, typed,      │   │ policy      │  │  │
│  │ ┌──────────────────┐   │   versioned, whois-     │   │ engine      │  │  │
│  │ │  cmesh daemon    │◄──┼── gated, :7778 ─────────┼──►│ + task store│  │  │
│  │ │ local gateway,   │   │                          │   └──────┬──────┘  │  │
│  │ │ outbound policy  │   │                          │          │ Runner  │  │
│  │ └──────────────────┘   │                          │   ┌──────▼──────┐  │  │
│  └────────────────────────┘                          │   │ headless    │  │  │
│        any node plays both roles;                    │   │ worker run  │  │  │
│        topology is symmetric                         │   └─────────────┘  │  │
│                                                      └────────────────────┘  │
└──────────────────────────────────────────────────────────────────────────────┘
```

One daemon per machine, two faces, two protocols.

### 6.1 Local MCP Gateway

The local MCP gateway is the single MCP registration per harness.

Properties:

- bound to loopback only;
- exposes the delegation tool surface;
- proxies to peers via mesh RPC;
- enforces outbound policy;
- writes outbound audit events;
- never exposes MCP remotely.

#### Local MCP hardening

Required in v1:

- bind only to loopback;
- validate `Host` and `Origin`;
- reject non-loopback values unless explicitly configured for a test harness;
- optional per-install random bearer token where the harness supports headers;
- never trust `X-Forwarded-*` or identity headers;
- never expose this surface via Tailscale Serve/Funnel, cloudflared, nginx, or another reverse proxy;
- no remote MCP mode.

### 6.2 Mesh RPC Server

The mesh RPC server is daemon-to-daemon only.

Properties:

- HTTP+JSON;
- typed request and response structs;
- schema-version header;
- idempotent task dispatch;
- whois-gated identity;
- policy-heavy;
- no remote MCP.

Required binding behavior:

- bind exclusively to the node's Tailscale IP, or to a tsnet listener;
- never bind mesh RPC to `0.0.0.0`;
- fail closed for non-tailnet source addresses before policy evaluation;
- expose a `/v1/hello` endpoint in Phase 1, even before dynamic discovery.

#### `/v1/hello`

```http
GET /v1/hello
```

Response:

```json
{
  "node_id": "node-01HZ...",
  "node_name": "homelab-vm",
  "schema_versions": ["v1"],
  "runners": [],
  "capabilities": [],
  "server_time": "2026-06-12T00:00:00Z"
}
```

Purpose:

- static-peer validation;
- version negotiation;
- basic health checks;
- future discovery support.

### 6.3 No Central Coordinator

There is deliberately no:

- broker;
- shared database;
- SaaS control plane;
- central scheduler.

Each daemon owns its local task store, peer config, runner config, and audit log. Topology is symmetric: whichever machine the user is sitting at can be the orchestrator.

---

## 7. Local MCP Tool Surface

Tool descriptions are written for the model. The model-facing API is intentionally small.

| Tool | Behavior |
|---|---|
| `list_peers()` | Live mesh nodes: name, OS, available runners, runner versions, capability tags, load, reachability. |
| `peer_info(peer)` | Capability manifest: runners and their profile manifests, allowed cwds, concurrency/queue state, toolchains. |
| `delegate(peer, prompt, cwd, runner?, profile?, timeout?, wait?, allow_dirty?)` | Dispatch a task. Returns `task_id` immediately by default. `wait=true` blocks up to timeout for short tasks. |
| `task_status(task_id)` | Returns state: `queued`, `running`, `done`, `failed`, `denied`, `cancelled`, plus elapsed time and last-activity summary. |
| `task_result(task_id)` | Returns the bounded canonical result envelope. |
| `cancel(task_id)` | Kills the worker subprocess/process group and marks task cancelled. |

### Canonical Error Taxonomy

Every tool failure returns exactly one machine-readable code plus a human-readable detail string.

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

Rules:

- new codes may be added;
- existing codes never change meaning;
- the delegation guidance artifact maps every code to a recommended agent response: retry, pick another peer, ask the human, or stop.

---

## 8. Identity and Authorization

### Trust-Boundary Doctrine

Network reachability, including authenticated tailnet membership, is defense in depth, never authorization.

Tailscale identity tells cmesh **who** connected. cmesh policy decides **what** that peer may do.

### Layers

1. **Network admission**
   - Tailscale grants allow tagged cmesh nodes to reach the mesh RPC port.
   - This is defense in depth only.

2. **Connection identity**
   - On every inbound RPC, the daemon queries tailscaled LocalAPI with `whois(remote_addr)`.
   - Unknown or untagged nodes are rejected without detail.
   - This is identity, not authorization.

3. **Task authorization**
   - This is the trust boundary.
   - Must hold even against a fully credentialed, whois-verified peer.

Policy dimensions:

- `allowed_peers`: which nodes may delegate to this node.
- `allowed_cwds`: which working directories may be used.
- `profiles`: effective permission ceilings.
- `runner`: which runner may execute.
- `network`: whether the effective profile allows network access.
- `git_push`: whether the effective profile allows pushing commits.
- `outbound_policy`: origin-side caps, such as denying delegation to `yolo` profiles.

4. **Execution containment**
   - The worker inherits the harness's permission model.
   - cmesh never passes skip-all-permissions flags.
   - Future OS sandboxing wraps the subprocess: Seatbelt on macOS, systemd sandboxing on Linux, restricted user/process isolation on Windows.

### Path Policy

`cwd` is resolved to a real path before policy evaluation.

The daemon must normalize:

- symlinks;
- `..`;
- case-insensitive filesystem behavior;
- drive-letter aliases;
- platform-specific path separators;
- user home expansion.

`allowed_cwds` matching occurs only after canonical realpath normalization.

---

## 9. Adversary Model

### 9.1 Injected Orchestrator

A local agent is prompt-injected and tries to delegate malicious work.

Mitigations:

- outbound policy at the origin daemon;
- inbound profile ceilings at the worker;
- hop limits;
- loud denials;
- full provenance;
- audit on both origin and worker.

### 9.2 Compromised Peer Node

A legitimate tailnet node submits validly authenticated hostile requests.

Mitigations:

- per-peer `allowed_peers`;
- cwd allowlists;
- profile ceilings;
- no trust based solely on whois;
- `yolo` profiles only on snapshot-protected nodes;
- audit on both ends.

### 9.3 Malicious Worker Results

`task_result` is a prompt-injection boundary. The origin model will read tool results, so worker-generated text is injection-bearing content no matter how it is labeled.

Mitigations:

- default result is a bounded canonical envelope;
- free text is confined to a length-capped `worker_summary_untrusted` field;
- full transcripts and raw worker output are never auto-inlined;
- transcript access is explicit and human/CLI-first in v1;
- shipped guidance states: **results are claims, not truth**;
- high-risk results require origin-side verification before acting;
- result envelope includes `verification_required` and `recommended_origin_checks`.

### 9.4 Untrusted Forward Task Prompts

Task prompts are untrusted input. Prompt injection can transit the mesh with a delegated task.

Deferred:

- prompt-content scanning;
- classification at the dispatch boundary;
- enforcement based on prompt risk.

The sequencing principle is observe first, enforce later.

---

## 10. Runners and Capability Manifests

A Runner is a per-harness adapter responsible for:

1. headless subprocess invocation;
2. profile translation into native harness controls;
3. result normalization into the mesh-standard result schema.

A node may offer multiple runners:

- `claude-code`;
- `codex`;
- future `opencode`;
- future `gemini-cli`;
- custom local commands.

Nothing outside `internal/runner/` may reference a harness by name.

### Profiles Are Evidence, Not Intent

Permission models are not isomorphic across harnesses. Each runner ships a `RunnerCapabilityManifest`.

Each profile declares:

- capability claims;
- exact native flags or controls used;
- enforcement confidence;
- known gaps;
- credential source;
- billing route;
- bare/non-bare behavior.

Example Claude Code manifest:

```json
{
  "runner": "claude-code",
  "version": "x.y.z",
  "profiles": {
    "readonly": {
      "claims": ["read_files"],
      "native_flags": ["--permission-mode", "plan", "--bare"],
      "credential_source": "ANTHROPIC_API_KEY|apiKeyHelper",
      "billing": "api",
      "enforcement_confidence": "medium",
      "known_gaps": [
        "bare mode loses CLAUDE.md and ambient project guidance",
        "non-bare runs load hooks, skills, plugins, inherited MCP servers, and CLAUDE.md"
      ]
    },
    "dev": {
      "claims": ["read_files", "edit_repo", "execute_repo_commands"],
      "native_flags": ["--allowedTools", "..."],
      "credential_source": "subscription_login",
      "billing": "subscription",
      "enforcement_confidence": "medium",
      "known_gaps": [
        "non-bare runs inherit ambient agent environment"
      ]
    }
  }
}
```

Example Codex manifest:

```json
{
  "runner": "codex",
  "profiles": {
    "dev": {
      "claims": ["read_files", "edit_repo", "execute_repo_commands"],
      "native_controls": {
        "sandbox": "workspace-write",
        "approval": "on-request",
        "network": "disabled_or_limited"
      },
      "enforcement_confidence": "medium",
      "known_gaps": [
        "approval and sandbox are separate axes",
        "network behavior depends on CLI configuration and wrapper"
      ]
    }
  }
}
```

### Bare Mode Caveat

Bare mode is deliberate and profile-controlled.

Non-bare workers load ambient agent environment:

- hooks;
- skills;
- plugins;
- inherited MCP servers;
- repo guidance files;
- user/session auth.

Bare workers are more reproducible and less ambiently privileged, but lose project guidance and require explicit headless credentials.

For Claude Code specifically:

- bare mode disables OAuth/keychain auth;
- bare mode does not read `CLAUDE_CODE_OAUTH_TOKEN`;
- bare mode requires `ANTHROPIC_API_KEY` or an `apiKeyHelper`;
- bare mode likely bills through API credentials rather than subscription login;
- if a bare profile credential source is unavailable, the runner returns `profile_unavailable`;
- the runner must never silently fall back from bare to non-bare.

Phase 0 must validate bare behavior per machine.

### Composition With Claw Patrol

Claw Patrol composition is a first-class runner option:

```toml
[runners.claude-code]
command = ["clawpatrol", "run", "--profile", "cmesh-dev", "--", "claude", "-p"]
```

Layering:

- **cmesh** decides who may ask this node to do what.
- **The harness** governs agent behavior within its native tool model.
- **Claw Patrol** gates what the process tree's network traffic may touch.
- **AgentSnitch** observes and attributes what actually happened.

The cmesh audit schema should be AgentSnitch-ingestible.

---

## 11. Task Lifecycle, Idempotency, and Restart Semantics

### Idempotency

Idempotency is part of the security model.

Rules:

- idempotency key = `origin_node_id + origin_session_id + task_id`;
- task IDs are high-entropy, such as ULID or UUIDv7;
- v1 hashes the raw validated request body for replay detection;
- if canonicalization is introduced later, use an explicit scheme such as RFC 8785 JSON Canonicalization Scheme;
- same key + same payload hash returns the existing task;
- same key + different payload hash returns `409 task_conflict` and audits loudly;
- same task ID from a different origin identity is rejected;
- completed-task idempotency records are retained for 30 days by default.

### State Machine

Worker durable state:

```text
queued → running → done
queued → running → failed
queued → denied
queued → cancelled
running → cancelled
```

Origin daemon mirrors task state for later lookup.

### Durability Guarantee

Once `delegate` returns:

- task state survives harness disconnect;
- task state survives MCP session loss;
- origin restart does not lose the task;
- worker restart reaches an explicit terminal state;
- `task_status` and `task_result` work from a later session;
- MCP stream resumability is a UX nicety, not a correctness dependency.

### Worker Restart Semantics

v1 is fail-closed.

Workers are spawned with:

- process group;
- pidfile;
- task-tagged environment variables;
- captured stdout/stderr;
- transcript/log reference.

On worker daemon restart:

- orphaned tasks are killed by process-group kill when possible;
- affected tasks are marked `failed`;
- error code is `worker_daemon_restarted`;
- silent loss is forbidden.

Supervisor-style reattach is a future upgrade.

---

## 12. Git and Result Contract

Artifacts move via git, not the mesh.

### Git Rules

- Worker refuses dirty worktrees unless `allow_dirty=true`.
- Changes land on deterministic branch: `cmesh/<task-id>/<slug>`.
- Worker commits locally by default.
- Pushing requires both `network` and `git_push` capability in the effective profile.
- Origin pulls through normal git remotes.
- No git repo means task may run, but changed files are reported as untracked local artifacts and are not transferred.

### Commit Identity

cmesh commits use a configured author identity, for example:

```text
cmesh@<node-name>
```

Commit messages include cmesh metadata footers:

```text
cmesh-task: 01HZ...
cmesh-origin: mac-studio
cmesh-worker: homelab-vm
cmesh-profile: dev
```

This makes audit-to-git correlation deterministic.

### Canonical Result Envelope

```json
{
  "status": "done",
  "branch": "cmesh/01HZ.../linux-build",
  "base_commit": "...",
  "head_commit": "...",
  "pushed": false,
  "diffstat": "...",
  "files_changed": ["..."],
  "tests": {
    "claimed_passed": true,
    "commands": ["go test ./..."],
    "logs_ref": "transcript:..."
  },
  "worker_summary_untrusted": "short, length-capped",
  "usage": {
    "tokens": 0,
    "cost": 0,
    "duration_s": 0
  },
  "verification_required": true,
  "recommended_origin_checks": [
    "git fetch homelab cmesh/...",
    "git diff --stat base..head",
    "go test ./..."
  ],
  "runner_extra": {}
}
```

The field is `tests.claimed_passed`, not `passed`, because the schema itself encodes that worker results are claims.

### Transcript Access

In v1, transcript access is CLI-first and human-oriented.

```bash
cmesh log <task_id> --worker
```

The MCP default result never auto-inlines full transcripts. A future explicit `task_transcript(task_id, offset?, limit?)` tool can be added if Phase 0 proves agents need bounded transcript slices.

---

## 13. Provenance and Loop Prevention

Every task carries:

- origin node, whois-verified;
- origin session ID;
- origin harness and version;
- worker harness and version;
- prompt SHA-256;
- timestamp;
- delegation chain;
- hop count;
- profile requested;
- profile granted;
- runner controls used.

Controls:

- hop limit default: 2;
- cycle detection: never re-enter a node already in the chain;
- denials are loud;
- audit on both origin and worker.

---

## 14. Orchestrator Integration and Delegation Heuristics

### `cmesh connect`

`cmesh connect` registers the loopback gateway with detected harnesses:

- Claude Code via `claude mcp add`;
- Codex via its MCP config;
- manual snippet printed for unrecognized harnesses.

One registration ever. Peers come and go without touching harness config.

### Delegation Guidance Artifact

cmesh ships one canonical guidance document, generated per harness:

- Claude Code skill;
- `AGENTS.md` fragment;
- future harness-specific formats.

Rules taught to the model:

- delegate OS-specific tasks;
- delegate long-running and self-contained tasks;
- delegate tasks requiring a worker's unique toolchain;
- route risky tasks only to snapshot/throwaway profiles;
- do not delegate tasks needing interactive credentials;
- do not delegate ambiguous product judgment;
- do not sub-delegate unless another machine concretely benefits the task;
- treat results as claims, not truth;
- verify high-risk results locally before acting.

The product fails through underuse or task-spraying, not transport. This artifact counters both.

---

## 15. Human Surface

v1 human surface is deliberately minimal:

```bash
cmesh status
cmesh log <task_id>
cmesh doctor
```

Features:

- TUI table of peers, running tasks, recent results;
- desktop notification on completion/denial;
- CLI access to logs and transcripts;
- no web dashboard in v1.

If a dashboard ever exists, it is whois-gated and still not the MCP surface.

---

## 16. Setup Experience

Assuming Tailscale is already up:

```bash
# 1. Install
curl -fsSL https://get.cmesh.dev | sh
# or: brew install / winget install / cargo binstall

# 2. Initialize
cmesh up --profiles readonly,dev

# 3. Register loopback gateway with detected harnesses
cmesh connect
```

Then, in any agent session:

```text
delegate the Linux build to homelab and keep working
```

Fresh machine to first successful delegation in more than five minutes is a release-blocking bug.

### Doctor Command

`cmesh doctor` validates setup:

```bash
cmesh doctor tailscale
cmesh doctor runner claude-code
cmesh doctor mcp
```

Expected Tailscale checks:

```text
tailscaled: reachable
localapi: reachable
tailnet_ip: 100.x.y.z
whois_self: ok
bind_tailnet_ip: ok
```

This explicitly catches the service-account failure mode where Tailscale works interactively but the daemon cannot access LocalAPI.

---

## 17. Implementation Stack

Language: Go.

Core dependencies and choices:

- single static binary per platform;
- `tailscale.com/client/local` for LocalAPI `WhoIs`, `WhoIsProto`, and status against host `tailscaled`;
- not `tailscale.com/client/tailscale`, which is the deprecated control-plane client;
- `tailscale.com/client/tailscale/v2` only if future key-minting automation needs control-plane mutation;
- official MCP Go SDK for the loopback gateway;
- `net/http` + JSON for mesh RPC;
- SQLite for task state;
- JSONL + SQLite for audit;
- Runner interface from v1;
- first runner: `internal/runner/claudecode`;
- tsnet mode later.

---

## 18. Risks and Open Questions

| Risk / Question | Position |
|---|---|
| Vendors ship native cross-machine teams | Plausible. Durable value is policy/provenance/audit on your hardware plus harness-agnosticism. Keep Runner thin. |
| Agents misuse delegation | Real product risk. Counter with heuristics artifact and outbound policy. |
| Worker permission prompts | v1: fit profile or fail with `denied`/`needs_human`. |
| Bare-mode auth and billing | Phase 0 validation item #1. `profile_unavailable` over silent fallback. Manifests declare billing route. |
| LocalAPI service-account access | Validate with `cmesh doctor tailscale`; do not assume interactive Tailscale access means daemon access. |
| Result-schema lowest common denominator | Required core fields plus `runner_extra`. Guidance relies only on core. |
| Enforcement overclaim | Manifests with confidence and known gaps. Audit requested/claimed/used. |
| Headless use vs provider ToS | Verify before public release. Each node uses its own auth. |
| Result-size blowups | Hard inline cap; transcripts by reference only. |
| Gateway vs N registrations | Decided: single loopback gateway. |
| Language | Decided: Go. |
| Mesh RPC flavor | Decided: HTTP+JSON, schema-versioned. |
| Naming | Vendor-neutral; collision-check before attachment. |

---

## 19. Success Metrics

- Fresh machine to first successful delegation in less than five minutes.
- At least five real delegations per week after week two.
- Zero tasks executed outside policy.
- 100% of dispatches present in both origin and worker audit logs.
- Zero credentials stored by cmesh in Tailscale mode.
- Zero enforcement claims above manifest confidence.
- Phase 1 restart test passes:
  - origin restart mid-task: result still retrievable;
  - worker restart mid-task: terminal `worker_daemon_restarted` state, no silent loss.
- By Phase 4, the orchestrating agent uses `delegate` correctly from shipped guidance alone on two harnesses, including verifying high-risk results.

---

## 20. Future Work

- Small-file artifact transfer, explicit and capability-gated.
- Permission-prompt escalation to origin.
- Cloud peers, such as disposable Fly.io or EC2 tailnet workers.
- cloudflared/webhook ingress for event-triggered delegation.
- Prompt-content policy at dispatch boundary.
- Team mode with per-user grants.
- Supervisor-style worker reattach after daemon restart.
- Explicit MCP `task_transcript` tool if Phase 0 proves agent-side transcript slices are necessary.
