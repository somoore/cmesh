# cmesh Implementation Plan

Status: Draft v1
Date: 2026-06-12

This plan turns the PRD into build phases, acceptance gates, and production constraints. The first useful product is Mac to Linux static-peer delegation with Claude Code, durable task state, policy enforcement, and audit.

## Guiding Constraints

1. MCP is local-only.
2. Daemon-to-daemon traffic is mesh RPC: HTTP+JSON over the tailnet.
3. Tailscale mode uses LocalAPI whois for identity and cmesh policy for authorization.
4. Policy is the trust boundary.
5. Runner manifests describe evidence-backed enforcement, known gaps, credential source, and billing.
6. Worker results are claims, not truth.
7. Every accepted task reaches a queryable terminal state.
8. Mac to Linux static peers ship before discovery, Windows, dashboards, or cloud workers.

## Repository Layout

```text
cmesh/
  cmd/
    cmesh/
      main.go
  internal/
    audit/
    config/
    doctor/
    gitx/
    identity/
      tailscale/
    mcp/
      gateway/
    mesh/
      rpc/
      schema/
    policy/
    runner/
      claudecode/
      codex/
    store/
    task/
    tui/
  schemas/
    audit.v1.json
    task.v1.json
    runner_manifest.v1.json
    result.v1.json
  docs/
    PRD.md
    PLAN.md
    delegation-guidance.md
    tailscale-grants.md
    runner-manifests.md
  phase0/
    delegations.jsonl
    prompts/
    results/
    notes.md
  examples/
    config/
      mac-studio.toml
      homelab-vm.toml
    grants/
      tailscale-policy.json
  scripts/
    install.sh
```

## Phase 0: Instrumented Manual Delegation

Duration: 3 to 5 days.

Goal: validate real usage patterns before daemon code. Use SSH plus headless worker harnesses over the existing tailnet.

Deliverables:

- `phase0/delegations.jsonl`;
- `phase0/notes.md`;
- prompt and result captures in `phase0/prompts/` and `phase0/results/`;
- bare-mode auth verdict per target machine;
- top delegation heuristics from real usage;
- decision on readonly bare vs non-bare default.

Required manual delegation categories:

1. Linux build or test from macOS origin.
2. Long-running compile.
3. Repo summarization or read-only task.
4. Risky operation on a snapshot VM.
5. Toolchain-specific task.
6. Task that fails due to missing permission.
7. Task that produces code changes.
8. Task that produces no code changes but gives a useful verdict.
9. Task where transcript or logs are useful.
10. Task where the result envelope is awkward or insufficient.

Bare-mode auth command:

```bash
claude -p --bare --output-format json "summarize this repo"
```

Exit criteria:

- at least ten logged delegations;
- result envelope validated against actual needs;
- bare-mode verdict per machine;
- Windows priority decision;
- top five delegation heuristics identified;
- readonly default decision recorded.

## Phase 1: Local Daemon, Static Peers, Mesh RPC, Claude Runner

Duration: 1 to 2 weeks.

Goal: working Mac to Linux delegation with static peers and no discovery.

Deliverables:

- `cmesh up`;
- `cmesh connect`;
- loopback MCP gateway;
- mesh RPC server bound to Tailscale IP only;
- `/v1/hello`;
- Tailscale LocalAPI identity using `tailscale.com/client/local`;
- inbound and outbound policy checks;
- Claude Code runner;
- SQLite task store;
- JSONL audit log;
- git branch and result contract;
- `cmesh status`;
- `cmesh log <task_id>`;
- `cmesh doctor tailscale`.

### Config

Node config shape:

```toml
[node]
name = "homelab-vm"
mesh_port = 7778
mcp_port = 7777

[tailscale]
mode = "host"
bind = "tailnet_ip"

[policy]
allowed_peers = ["mac-studio"]
allowed_cwds = ["~/projects/**"]

[profiles.readonly]
runner = "claude-code"
claims = ["read_files"]

[profiles.dev]
runner = "claude-code"
claims = ["read_files", "edit_repo", "execute_repo_commands"]

[runners.claude-code]
command = ["claude", "-p"]
default_profile = "dev"
```

### Doctor

`cmesh doctor tailscale` checks:

- tailscaled reachable;
- LocalAPI reachable;
- daemon user can query LocalAPI;
- tailnet IP discovered;
- whois self works;
- mesh bind to tailnet IP works;
- mesh bind is not 0.0.0.0.

Expected healthy output:

```text
tailscaled: reachable
localapi: reachable
tailnet_ip: 100.x.y.z
whois_self: ok
bind_tailnet_ip: ok
```

### Mesh RPC

Endpoints:

```text
GET  /v1/hello
POST /v1/tasks
GET  /v1/tasks/{task_id}
GET  /v1/tasks/{task_id}/result
POST /v1/tasks/{task_id}/cancel
```

Requirements:

- schema-version header;
- tailnet IP bind only;
- source address must be tailnet;
- inbound whois identity check;
- inbound policy after identity;
- idempotent task dispatch;
- audit events for request, acceptance, denial, start, completion, failure, cancellation, and result creation.

### Local MCP Gateway

Tools:

```text
list_peers()
peer_info(peer)
delegate(peer, prompt, cwd, runner?, profile?, timeout?, wait?, allow_dirty?)
task_status(task_id)
task_result(task_id)
cancel(task_id)
```

Hardening:

- loopback bind only;
- Host and Origin validation;
- forwarded identity headers ignored;
- no remote mode.

### Store

SQLite tables:

```sql
tasks
task_events
idempotency_keys
peer_tasks
runner_manifests
```

Minimum task fields:

- task_id;
- origin_node_id;
- origin_session_id;
- worker_node_id;
- state;
- cwd;
- runner;
- profile_requested;
- profile_effective;
- prompt_hash;
- payload_hash;
- created_at;
- updated_at;
- terminal_error_code;
- result_ref.

### Runner: Claude Code

Responsibilities:

- build command from config;
- choose bare or non-bare from profile;
- validate bare credentials before spawn;
- return `profile_unavailable` when a bare credential is unavailable;
- never pass skip-all-permissions flags;
- capture stdout and stderr;
- record transcript/log reference;
- normalize to `schemas/result.v1.json`.

### Git Contract

Worker behavior:

- normalize realpath before policy;
- refuse dirty worktrees unless `allow_dirty=true`;
- create branch `cmesh/<task-id>/<slug>`;
- commit locally by default;
- use configured cmesh author;
- include cmesh metadata footers;
- return diffstat and changed files;
- push only if effective profile grants network and git_push.

### Restart Semantics

Worker daemon:

- workers run in process groups;
- task pidfiles are written;
- stdout and stderr are captured;
- task-tagged environment variables are set;
- startup detects interrupted running tasks;
- known process groups are killed when possible;
- interrupted tasks become `failed` with `worker_daemon_restarted`.

Origin daemon:

- mirrored peer tasks are reloaded on startup;
- status and result lookup survive restart.

Phase 1 acceptance:

1. Run `cmesh doctor tailscale` on both nodes.
2. Register MCP gateway with the local harness.
3. Delegate a Linux build from Mac to homelab.
4. Confirm `delegate` returns a task ID.
5. Continue local work while task runs.
6. Poll `task_status`.
7. Fetch `task_result`.
8. Pull the worker branch through git.
9. Review diffstat and rerun verification locally.
10. Confirm audit events exist on both nodes.
11. Restart origin mid-task and confirm result remains retrievable.
12. Restart worker mid-task and confirm terminal `worker_daemon_restarted`.

## Phase 2: Policy and Provenance Hardening

Duration: 1 week.

Goal: make the security model honest and observable before discovery.

Deliverables:

- RunnerCapabilityManifest schema;
- audit schema with requested, granted, and used controls;
- profile ceilings;
- outbound origin policy;
- hop limit;
- cycle detection;
- full provenance envelope;
- AgentSnitch-aligned audit JSONL.

Acceptance:

- profile ceiling violations are denied before runner spawn;
- hop count above limit is denied;
- cycles are denied;
- audit includes requested, granted, and used controls;
- sample audit event can be ingested by downstream tooling.

## Phase 3: Discovery and UX

Duration: 1 week.

Goal: remove static-peer friction.

Deliverables:

- `list_peers`;
- `peer_info`;
- discovery through Tailscale status plus `/v1/hello`;
- `cmesh connect`;
- generated delegation guidance;
- `cmesh status` TUI;
- desktop notifications.

Acceptance:

- new peer appears without editing harness config;
- agent can choose based on `list_peers` and `peer_info`;
- guidance causes appropriate delegation without human coaching;
- status view shows running tasks and recent results.

## Phase 4: Codex Runner and Abstraction Proof

Duration: 1 to 2 weeks.

Goal: prove the Runner abstraction.

Deliverables:

- Codex runner;
- Codex runner manifest;
- cross-harness acceptance test;
- `wait=true` fast path;
- concurrency caps and queue depth;
- raw WireGuard manual mode documentation;
- tsnet prototype if it can be added without destabilizing Phase 1 behavior.

Acceptance:

- Claude Code session delegates to Codex worker;
- Codex session delegates to Claude Code worker;
- core code stays runner-agnostic;
- Codex manifest declares sandbox, approval, and network axes;
- guidance works across both harnesses.

## Phase 5: Windows and Containment Polish

Duration: 1 to 2 weeks.

Windows moves earlier if Phase 0 shows at least 20 percent of useful delegations need it.

Deliverables:

- Windows daemon;
- Windows worker support;
- service install;
- PowerShell-safe spawning and quoting;
- drive-letter, case, symlink, and separator normalization;
- process-tree kill semantics;
- optional WSL2 worker config;
- OS sandbox wrapper evaluation.

Acceptance:

- Windows worker executes native task;
- denied paths cannot be reached through alias/case/symlink tricks;
- cancellation kills the process tree;
- worker restart yields a terminal state.

## Phase 6: Launch Decision

Decide whether cmesh remains a personal tool, ships as OSS, or becomes a product surface.

Personal-tool optimization:

- Mac/Linux reliability;
- minimal maintenance;
- daily delegation habit;
- AgentSnitch integration.

OSS launch claim:

```text
Policy-governed, provenance-rich delegation between local coding agents across machines you control.
```

Product differentiators:

- policy;
- provenance;
- audit;
- local-first operation;
- harness-agnostic runners;
- composition with network and process observation tools.

## Core Test Matrix

Platforms:

| Platform | Phase | Required |
| --- | ---: | --- |
| macOS | 1 | yes |
| Linux | 1 | yes |
| Windows | 5 or earlier | conditional |
| WSL2 | 5 | optional |

Runners:

| Runner | Phase | Required |
| --- | ---: | --- |
| Claude Code | 1 | yes |
| Codex CLI | 4 | yes |
| opencode | future | no |
| Gemini CLI | future | no |

Core tests:

- Local MCP binds loopback only.
- Mesh RPC binds tailnet IP only.
- Non-tailnet source fails closed.
- LocalAPI is accessible under the service account.
- whois identity check works.
- Static peer delegation works.
- Idempotent retry returns same task.
- Mutated replay returns `task_conflict`.
- Same task ID from different origin rejects.
- Dirty worktree rejects unless `allow_dirty=true`.
- Denied cwd rejects after realpath normalization.
- Runner unavailable returns a stable code.
- Profile unavailable returns a stable code.
- Worker restart reaches terminal state.
- Origin restart preserves lookup.
- Result envelope length cap is enforced.
- Transcript is not auto-inlined.
- Audit exists on both origin and worker.
