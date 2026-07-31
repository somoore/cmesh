# Teams: implementation plan

> Status: plan of record. This document describes work that is partly unbuilt.
> Anything here that is not yet implemented is marked **planned**. Do not cite a
> planned feature in user-facing docs until it ships.

cmesh today delegates a bounded task to one peer and returns an auditable
envelope. The destination is a **team**: coding agents from different vendors,
on different machines in a tailnet, working the same problem under one policy
boundary, with a durable record of who did what.

This document is the sequenced plan to get there, the design decisions behind
it, and the invariants that must survive every phase.

## The model: conductor and instruments

There are two ways to build agent teams. cmesh commits to the second.

**Free-form agent-to-agent chat** — agent A messages agent B, B messages C — is
the intuitive reading of "team" and is deliberately **not** what cmesh builds.
Cross-vendor agent chat is a prompt-injection amplifier: one agent's output
becomes the next agent's instructions, and every hop launders untrusted text
into a trusted prompt. It also dissolves the audit story, because "who decided
this" stops having an answer.

**Conductor and instruments** is the model cmesh already implements. One
orchestrating agent — the session the human is in — delegates bounded tasks.
Workers return evidence. Work products move between machines as git branches and
structured envelopes, never as prose fed into another agent's prompt. The team
conversation is the origin session; the shared memory is the repository.

This is how good human teams work through pull requests, and it is what lets
agents from vendors that have no reason to trust each other collaborate safely.
The security posture is the team feature, not a limitation on it.

## The five primitives

| Primitive | Verb | Status |
| --- | --- | --- |
| Delegation | `delegate` | shipped |
| Progress events | `task_events` | **planned** (Phase 1) |
| Task channel | `task_post` / `task_ask` / `task_send` | **planned** (Phase 2) |
| Cross-peer handoff | `handoff` | **planned** (Phase 3) |
| Workstreams | `workstream` / `workstream_status` | **planned** (Phase 4) |

## Design insight: the channel splits in two

The task channel — the ability to talk to a task while it runs — has two halves
with very different costs, and separating them is what makes this tractable.

**Worker → origin** (progress notes, questions, approval requests) needs **no
harness streaming at all**. A worker task already runs with a task-scoped cmesh
MCP gateway injected and a per-task token bound to it. A worker asking a
question is just another MCP tool call over infrastructure that already exists
and is already authorization-scoped. This works for every MCP-capable runner —
Claude Code and Codex both, today.

**Origin → worker** live steering (the `/btw`-into-a-running-session motion)
requires reworking `exec.go` to keep stdin open and consume incremental output,
plus per-harness streaming support (`--input-format stream-json` for Claude
Code, proto mode for Codex).

So the plan ships the worker→origin half first (Phase 2a) for roughly 80% of the
value at a fraction of the cost, and treats live steering (Phase 2b) as a
separable follow-on. Runners that cannot stream still receive queued messages at
the next `task_ask` checkpoint, which is genuinely useful on its own.

## Phases

Each phase ships something usable and depends only on earlier phases.

### Phase 0 — Coherence cleanup

Shrink the surface before adding to it.

- Remove the REST `POST /tools/<name>` handler subset from the gateway. It is a
  second, partial entry path to the same tools with no consumer (no test, no CLI
  caller). JSON-RPC on `/mcp` is the MCP contract, and one path is easier to keep
  hardened.
- Consolidate `register_gateway` (stdio wizard) and `register_harness` (gateway)
  onto one shared implementation. They both point a harness at the local gateway
  but differ in harness coverage and in whether they remove the stdio entry
  first. The gateway version gains Codex support. Both tool names stay, with
  identical behavior, so no caller breaks.
- Replace the literal `"dev"` fallback in `checkProfile` with `query`. Every
  other default in the system is `query`; this is the one place the legacy world
  leaks into policy.
- Docs: use canonical profile names everywhere (`query`, `query-network`,
  `query-shell`, `readonly-agent`, `repo-dev`, `desktop-control`,
  `system-change`). `network` and `git_push` are capabilities and flags, not
  rungs on the ladder. `dev` / `readonly` / `interactive` are compatibility
  aliases, documented as such in exactly one place.

### Phase 1 — Progress events

Foundation for the channel. No new trust surface: this exposes a table that is
already written on every task transition.

- Store: `ListTaskEvents(taskID, afterSeq, limit)` with stable ordering (a `seq`
  column, migration v3).
- Mesh RPC: `GET /v1/tasks/{id}/events?after=&wait=`, long-polling through the
  existing wait machinery, ownership-scoped exactly like status.
- Gateway: `task_events` tool routed through `fetchOwnedTask`, so per-task token
  scoping applies automatically.

A conductor can then narrate real progress — "Linux tests running, Windows build
queued, review done" — instead of polling terminal state.

### Phase 2a — Task channel, worker → origin

The high-leverage phase.

- Store: `task_messages` table — task_id, seq, direction, kind
  (`note` | `question` | `approval_request` | `answer`), `body_untrusted`
  (16 KiB cap), created_at, optional client message id for idempotency. Per-task
  message cap (~200). Every message mirrored to the audit log.
- Task state `awaiting_input`: `running <-> awaiting_input`, and
  `awaiting_input -> failed | cancelled`. `failClosedRestart` treats it exactly
  like `running`. A worker question stops requiring the task to die: today
  `needs_human` is terminal, which is the wrong shape for a colleague.
- Worker-side task-scoped tools, authenticated by the existing per-task token:
  `task_post` (progress note) and `task_ask` (question; the task enters
  `awaiting_input` and the call blocks for an answer up to a timeout, then
  returns "unanswered — continue or wait").
- Mesh RPC: `GET /v1/tasks/{id}/messages?after=&wait=` for the origin to pull,
  `POST /v1/tasks/{id}/messages` for the origin to answer or steer. Queued
  messages are delivered at the next `task_ask` checkpoint. Ownership-scoped and
  schema-versioned like every other endpoint.
- Origin gateway tools: `task_send`, `task_messages`.

**Known risk:** harness MCP per-call timeouts versus a blocking `task_ask`.
Mitigated two ways — the adapter sets the tool-timeout environment variable at
launch, and `task_ask` supports poll-style resume (calling it again returns a
pending answer rather than opening a second question).

### Phase 2b — Inbound live steering

Separable and larger.

- `exec.go` grows an interactive run path: live stdin writer, incremental output
  limits. The existing batch path is untouched.
- The claudecode adapter uses `--input-format stream-json --output-format
  stream-json` when the manifest declares support; queued `task_send` messages
  inject as user turns. Codex follows via its proto mode.
- Manifest field `mid_task_input: none | checkpoint | stream`, declared honestly
  per runner and verified **behaviorally** by `cmesh doctor runner`, in keeping
  with the manifests-are-evidence rule.

### Phase 3 — Cross-peer handoff

The core team motion: Claude on the Linux box writes the fix, Codex on the
Windows box verifies it natively.

Today `followup_task_id` deliberately pins a follow-up to the same peer, and the
git bundle only travels worker → origin, so this requires the human's agent to
fetch a bundle and re-seed by hand.

- `handoff`: resolve the source task, fetch its bundle, upload the seed via a new
  size-capped `POST /v1/tasks/{id}/artifacts/git-bundle`, and have the receiving
  worker apply it onto the base commit.
- Lineage: a `handoff_from` field on the task record, in `task.v1.json`, in the
  audit event, and in the commit footer — so provenance is verifiable from the
  git object alone.
- Typed failure `handoff_base_missing` when the receiving peer's checkout lacks
  the base commit, with actionable guidance (shared remote, or a `git_push`
  profile). One clear denial path, never a best-effort fallback.
- No new authority: outbound policy at the origin plus inbound policy at the
  receiving peer, identical to any other delegation.

### Phase 4 — Workstreams

`origin_session_id` already groups a delegation thread and is now an
authorization key, but it is invisible plumbing. Name it.

- A `workstream` parameter on `delegate` (validated slug mapped to a session id),
  a `list_tasks` filter, and `workstream_status` aggregating per-peer state in
  one call.
- Document the deliberate interplay with token scoping: children in a workstream
  share a session, so teammates within one can see each other's tasks. That is
  the team-visibility boundary, and naming it turns an implementation detail into
  a documented feature.
- **Explicitly not** cross-node runner session resumption. The setup wizard's
  "there is no cross-node session" rule is correct; the portable state is the
  branch, not a harness's hidden memory.

### Phase 5 — Generic runner adapter

Two hardcoded adapters caps the multi-vendor story at two vendors. The
invariants the adapters actually enforce are small and portable.

- `internal/runner/generic`: config-driven command, per-profile flag mappings for
  the writes and shell axes, stdin-only prompt delivery (enforced, not
  requested), a required `manifest_path`, and structured-output field mapping
  with a raw fallback.
- `cmesh doctor runner <name>` runs the same behavioral smoke used for the
  built-in adapters, so runner honesty is verified empirically rather than
  declared.
- `enforcement_confidence: high` remains impossible for any non-probe runner, so
  a third-party runner cannot overclaim.

## Invariants

These hold in every phase. A change that breaks one is wrong regardless of what
it enables.

1. **Messages are task events** — durable, bounded, audited. No side channel.
2. **Worker-to-origin bodies are untrusted.** `body_untrusted` is the API, the
   same way `worker_summary_untrusted` is.
3. **Steering and answer authority equal task ownership** — the origin-session
   scoping that governs task reads governs the channel too.
4. **Approvals are typed and policy-checked.** An `approval_request` carries the
   tool and command, so the origin can mechanically deny anything outside the
   profile's granted capabilities. Free-text questions that touch authority go to
   the human and are never auto-answered.
5. **No worker-to-worker channel, ever.** Handoff moves branches and refs, never
   prose.
6. **Manifest honesty extends to new capabilities** — `mid_task_input` and
   generic runners are declared per runner and verified behaviorally.

## Documentation program

- `docs/teams.md` — written after Phase 2a: the conductor model, the five
  primitives, the transitive-injection rule, and one worked end-to-end example.
- README and `docs/concepts.md` reframe — deliberately **after** Phases 1–3 ship,
  so the headline never outruns the demo.
- `ycmesh.md` — add the two missing comparisons: versus one agent with many MCP
  tools, and versus in-process multi-agent frameworks. The differentiator is real
  machines, real credentials, and real policy rather than role-play between
  prompts sharing one process.
- `CLAUDE.md` — tool counts, task states, and standing invariants updated in the
  phase that changes them.

## See also

- [Architecture](architecture.md) — the request lifecycle these primitives extend.
- [Security](security.md) — the trust boundary every phase must preserve.
- [Roadmap](../ROADMAP.md) — how these phases map onto Now / Next / Later.
