# cmesh PLAN.md

Execution plan for cmesh: a local-first delegation plane for coding agents.

This plan is intentionally implementation-oriented. The PRD defines the product and security model; this file defines the build sequence, validation gates, and concrete tasks.

---

## Guiding Constraints

1. **MCP is local-only.** No remote MCP in v1.
2. **Daemon-to-daemon is mesh RPC.** HTTP+JSON over tailnet, versioned and idempotent.
3. **Tailscale mode does not use daemon-to-daemon bearer tokens.** Identity comes from LocalAPI `whois`; authorization comes from cmesh policy.
4. **Policy is the trust boundary.** Tailnet grants/tags are defense in depth.
5. **Runner enforcement claims must be honest.** Manifests declare controls, gaps, confidence, credential source, and billing.
6. **Results are claims, not truth.** Worker output is prompt-injection-bearing content.
7. **No silent loss.** Every task reaches a queryable terminal state.
8. **Ship the smallest useful thing first.** Mac ↔ Linux with static peers before discovery, Windows, or dashboards.

---

## Proposed Repository Layout

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

---

## Phase 0 — Instrumented Manual Delegation

**Duration:** 3–5 days  
**Goal:** validate real usage patterns, result needs, bare-mode feasibility, and Windows priority before writing daemon code.

### Scope

Use SSH plus headless Claude Code over the existing tailnet. No daemon code.

### Create Phase 0 Files

```bash
mkdir -p phase0/prompts phase0/results
touch phase0/delegations.jsonl phase0/notes.md
```

### Delegation Record Schema

Each manual delegation gets one JSONL record.

```json
{
  "id": "phase0-001",
  "timestamp": "2026-06-12T00:00:00Z",
  "origin_machine": "mac-studio",
  "worker_machine": "homelab-vm",
  "reason_for_delegation": "linux build",
  "cwd": "~/projects/example",
  "desired_profile": "dev",
  "prompt_file": "phase0/prompts/phase0-001.md",
  "expected_result_shape": ["summary", "branch", "test_log", "diffstat"],
  "actual_result_shape": ["summary", "test_log"],
  "files_changed": [],
  "human_intervention_needed": false,
  "time_to_result_s": 0,
  "would_delegate_again": true,
  "awkwardness_notes": ""
}
```

### Required Bare-Mode Auth Test

Run on each target machine:

```bash
claude -p --bare --output-format json "summarize this repo"
```

Record:

- machine;
- Claude Code version;
- whether bare mode worked;
- credential source:
  - `ANTHROPIC_API_KEY`;
  - `apiKeyHelper`;
  - neither;
- observed billing route:
  - API;
  - subscription;
  - unknown;
- failure text;
- whether non-bare worked.

### Required Manual Delegation Types

Perform at least ten real delegations across these categories where possible:

1. Linux build/test from macOS origin.
2. Long-running compile.
3. Repo summarization/read-only task.
4. Risky operation on snapshot VM.
5. Toolchain-specific task.
6. Task that fails due to missing permission.
7. Task that produces code changes.
8. Task that produces no code changes but useful verdict.
9. Task where transcript/log is useful.
10. Task where result envelope is insufficient or awkward.

### Phase 0 Exit Criteria

- At least ten logged delegations.
- Draft result envelope validated against actual needs.
- Bare-mode verdict per machine.
- Notes on whether Windows should move earlier.
- List of top five delegation heuristics that actually mattered.
- Decision: `readonly` default bare or non-bare for v1.

---

## Phase 1 — Local Daemon, Static Peers, Mesh RPC, Claude Runner

**Duration:** 1–2 weeks  
**Goal:** working Mac ↔ Linux delegation with no discovery and no second runner.

### Phase 1 Deliverables

- `cmesh up`
- `cmesh connect`
- loopback MCP gateway
- mesh RPC server bound to Tailscale IP only
- static peer config
- `/v1/hello`
- Tailscale LocalAPI identity via `tailscale.com/client/local`
- policy checks:
  - peer allowed;
  - cwd allowed;
  - profile available;
  - runner available;
- ClaudeCodeRunner
- SQLite task store
- JSONL audit log
- git branch/result contract
- `cmesh status`
- `cmesh log <task_id>`
- `cmesh doctor tailscale`

### Implementation Tasks

#### 1. Config

Define node config.

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

#### 2. Doctor: Tailscale

Implement:

```bash
cmesh doctor tailscale
```

Checks:

- `tailscaled` reachable;
- LocalAPI reachable;
- daemon user can query LocalAPI;
- tailnet IP discovered;
- whois self works;
- mesh bind to tailnet IP works;
- bind to `0.0.0.0` is not used.

Expected output:

```text
tailscaled: reachable
localapi: reachable
tailnet_ip: 100.x.y.z
whois_self: ok
bind_tailnet_ip: ok
```

#### 3. Mesh RPC

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
- bind tailnet IP only;
- check source is tailnet;
- call LocalAPI whois for inbound requests;
- reject unknown identity;
- enforce inbound policy after identity;
- write audit events.

#### 4. Idempotency

Implement for `POST /v1/tasks`.

Rules:

```text
idempotency key = origin_node_id + origin_session_id + task_id
payload_hash = sha256(raw_request_body)
```

Behavior:

- same key + same payload hash returns existing task;
- same key + different payload hash returns `409 task_conflict`;
- same task ID from different origin identity rejects;
- retention default: 30 days.

#### 5. Local MCP Gateway

Expose tools:

```text
list_peers()
peer_info(peer)
delegate(peer, prompt, cwd, runner?, profile?, timeout?, wait?, allow_dirty?)
task_status(task_id)
task_result(task_id)
cancel(task_id)
```

Hardening:

- loopback only;
- validate Host/Origin;
- reject forwarded identity headers;
- no remote mode.

#### 6. Store

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

#### 7. Audit

Append JSONL events and mirror key events in SQLite.

Event types:

```text
task.dispatch.requested
task.dispatch.accepted
task.dispatch.denied
task.started
task.completed
task.failed
task.cancelled
policy.denied
runner.spawned
runner.exited
result.created
```

Every event includes:

- event_id;
- timestamp;
- node_id;
- task_id;
- peer identity;
- origin session;
- chain;
- runner;
- profile;
- policy decision;
- source.

#### 8. Runner: Claude Code

Implement `internal/runner/claudecode`.

Responsibilities:

- build command;
- choose bare/non-bare based on profile;
- validate credential source for bare profiles;
- return `profile_unavailable` if bare credential missing;
- never pass skip-all-permissions flags;
- capture stdout/stderr;
- capture transcript/log ref;
- normalize result envelope.

#### 9. Git Contract

Implement:

- realpath cwd normalization;
- dirty worktree refusal unless `allow_dirty=true`;
- deterministic branch `cmesh/<task-id>/<slug>`;
- local commit by default;
- cmesh commit author;
- cmesh metadata footers;
- diffstat and changed files in result;
- push only if profile has `network` and `git_push`.

#### 10. Restart Semantics

Implement v1 fail-closed worker restart:

- process group;
- pidfile;
- task-tagged env vars;
- captured output;
- on daemon startup, detect running/orphaned tasks;
- kill process group when possible;
- mark `failed` with `worker_daemon_restarted`.

Origin restart:

- reload mirrored peer tasks;
- `task_status` continues to work;
- `task_result` continues to work if worker has terminal result.

### Phase 1 Acceptance Test

From Mac Studio:

1. Run `cmesh doctor tailscale` on both nodes.
2. Register MCP gateway with Claude Code.
3. Ask origin agent: “delegate the Linux build to homelab and keep working.”
4. Confirm `delegate` returns task ID.
5. Continue local work while task runs.
6. Poll `task_status`.
7. Fetch `task_result`.
8. Pull branch through git.
9. Review diffstat and rerun verification locally.
10. Confirm audit events exist on both nodes.

Restart tests:

- restart origin daemon mid-task; result remains retrievable;
- restart worker daemon mid-task; task reaches `failed / worker_daemon_restarted`;
- no silent loss.

---

## Phase 2 — Policy and Provenance Hardening

**Duration:** 1 week  
**Goal:** make the security model honest and observable before dynamic discovery.

### Deliverables

- RunnerCapabilityManifest schema.
- Requested/claimed/used audit fields.
- Profile ceilings.
- Outbound origin policy.
- Hop limit.
- Cycle detection.
- Full provenance envelope.
- AgentSnitch-aligned audit schema.
- Sample AgentSnitch ingestion.

### Tasks

1. Define `schemas/runner_manifest.v1.json`.
2. Define `schemas/audit.v1.json`.
3. Add manifest loading and validation.
4. Add profile ceilings:
   - requested profile;
   - effective profile;
   - denied reason.
5. Add origin-side outbound policy:
   - deny profiles;
   - deny peers;
   - deny subdelegation;
   - deny risky profiles from non-snapshot machines.
6. Add provenance chain:
   - origin node;
   - origin session;
   - hop count;
   - chain nodes;
   - prompt hash.
7. Add hop limit default 2.
8. Add cycle detection.
9. Add AgentSnitch ingest example.

### Acceptance Criteria

- A request exceeding profile ceiling is denied before runner invocation.
- A request with hop count above limit is denied.
- A request that cycles back to a node in the chain is denied.
- Audit log includes requested/claimed/used controls.
- AgentSnitch can ingest sample audit event.

---

## Phase 3 — Discovery and UX

**Duration:** 1 week  
**Goal:** remove static-peer friction and make the product pleasant enough to use daily.

### Deliverables

- `list_peers`
- `peer_info`
- daemon hello integration
- peer discovery via Tailscale status + `/v1/hello`
- `cmesh connect`
- generated delegation guidance
- `cmesh status` TUI
- desktop notifications

### Tasks

1. Use Tailscale status to enumerate candidate peers.
2. Query `/v1/hello` for cmesh peers.
3. Merge peer status:
   - online/offline;
   - OS;
   - runners;
   - profile manifests;
   - queue depth;
   - current load.
4. Implement `cmesh connect`.
5. Generate:
   - Claude Code skill;
   - `AGENTS.md` fragment;
   - generic MCP guidance.
6. Implement TUI status.
7. Add notifications on:
   - done;
   - failed;
   - denied;
   - needs_human.

### Acceptance Criteria

- New peer appears without editing harness config.
- Agent can choose a peer based on `list_peers` and `peer_info`.
- Guidance causes the agent to delegate appropriately without human coaching.
- Status TUI shows running tasks and recent results.

---

## Phase 4 — Codex Runner and Abstraction Proof

**Duration:** 1–2 weeks  
**Goal:** prove the Runner abstraction is real.

### Deliverables

- `internal/runner/codex`
- Codex RunnerCapabilityManifest
- cross-harness acceptance test
- `wait=true` fast path
- concurrency caps and queue depth
- manual WireGuard mode docs
- tsnet prototype if time allows

### Tasks

1. Implement Codex runner invocation.
2. Map Codex native controls:
   - sandbox;
   - approval;
   - network.
3. Declare gaps honestly.
4. Add tests ensuring no code changes outside runner package and config.
5. Implement `wait=true` for short tasks.
6. Add per-node concurrency cap.
7. Add queue depth to `peer_info`.
8. Document raw WireGuard manual mode.

### Acceptance Criteria

- Claude Code session on Mac delegates to Codex worker on homelab.
- Codex session delegates to Claude Code worker.
- No core code changes beyond runner registration/config.
- Codex manifest declares sandbox/approval/network axes.
- Agent guidance still works on both harnesses.

---

## Phase 5 — Windows and Containment Polish

**Duration:** 1–2 weeks  
**Goal:** make Windows useful and harden subprocess execution.

### Sequencing Rule

If Phase 0 shows at least 20% of real delegations want the Windows box, Phase 5 swaps with Phase 4. Daily utility beats abstraction proof for the n=1 tool.

### Deliverables

- Windows daemon.
- Native Windows worker support.
- Service install.
- PowerShell-safe spawning and quoting.
- Path normalization for drive letters, case, symlinks.
- Process-tree kill semantics.
- WSL2 optional worker config.
- Per-OS sandbox wrapping pass.

### Tasks

1. Implement Windows service install.
2. Implement Windows process group/job object handling.
3. Normalize Windows paths before policy checks.
4. Add native Claude Code worker test.
5. Add optional WSL2 runner config.
6. Evaluate:
   - macOS Seatbelt wrapper;
   - Linux systemd sandboxing;
   - Windows restricted user/job object.
7. Extend manifests to declare OS wrapper controls.

### Acceptance Criteria

- Windows worker executes native task.
- Denied paths cannot be reached via alias/case/symlink tricks.
- Cancelling a task kills process tree.
- Worker restart yields terminal state, not silent loss.

---

## Phase 6 — Decision Checkpoint

**Goal:** decide whether cmesh remains a personal tool, becomes OSS, or becomes a Moore.cloud product surface.

### If Personal Tool

Optimize for:

- minimal maintenance;
- Mac/Linux reliability;
- daily delegation habit;
- AgentSnitch integration.

### If OSS

README demo:

```text
three machines, one prompt, watch the task hop
```

Launch claim:

```text
policy-governed, provenance-rich delegation between local coding agents across machines you control
```

Avoid “nobody else does this” claims.

### If Product

Differentiator:

- policy;
- provenance;
- audit;
- local-first;
- harness-agnostic;
- AgentSnitch + Claw Patrol composition.

---

## Test Matrix

### Platforms

| Platform | Phase | Required |
|---|---:|---|
| macOS | 1 | yes |
| Linux | 1 | yes |
| Windows | 5 or earlier | conditional |
| WSL2 | 5 | optional |

### Runners

| Runner | Phase | Required |
|---|---:|---|
| Claude Code | 1 | yes |
| Codex CLI | 4 | yes |
| opencode | future | no |
| Gemini CLI | future | no |

### Core Tests

- Local MCP binds loopback only.
- Mesh RPC binds tailnet IP only.
- Non-tailnet source fails closed.
- LocalAPI accessible under service account.
- whois identity check works.
- Static peer delegation works.
- Idempotent retry returns same task.
- Mutated replay returns `task_conflict`.
- Different origin with same task ID rejects.
- Dirty worktree rejects unless `allow_dirty=true`.
- Denied cwd rejects after realpath normalization.
- Runner unavailable returns stable error code.
- Profile unavailable returns stable error code.
- Worker restart reaches terminal state.
- Origin restart preserves lookup.
- Result envelope length cap enforced.
- Transcript not auto-inlined.
- Audit exists on both ends.

---

## Phase 0 Delegation Heuristics to Validate

Track whether these rules actually match real use:

1. Delegate OS-specific work.
2. Delegate long-running self-contained tasks.
3. Delegate unique-toolchain tasks.
4. Delegate risky operations to snapshot/throwaway profile.
5. Do not delegate ambiguous judgment.
6. Do not delegate interactive-credential tasks.
7. Do not subdelegate unless machine locality matters.
8. Treat worker results as claims.
9. Verify high-risk diffs locally.
10. Prefer git branch handoff over transcript handoff.

---

## Open Questions to Resolve With Data

1. Is bare mode viable on all machines?
2. Does bare mode break repo-context quality too much?
3. Does API billing make bare readonly unattractive?
4. What result fields do real delegations need?
5. How often are transcripts actually needed?
6. How often do tasks need human permission prompts?
7. Is Windows important enough to pull earlier?
8. Does git-only artifact movement feel natural?
9. Do agents underuse or overuse delegation with guidance alone?
10. Is `wait=true` needed in Phase 1 or can it wait?

---

## Immediate Next Actions

1. Create repo.
2. Add `docs/PRD.md` and `PLAN.md`.
3. Create `phase0/` folder.
4. Run bare-mode auth test on each machine.
5. Log ten manual delegations.
6. Revisit result envelope based on Phase 0 data.
7. Begin Phase 1 only after Phase 0 exit criteria are met.
