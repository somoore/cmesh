# Roadmap

cmesh is experimental software for trusted private networks. This roadmap sets expectations for the hardening path without implying that the current release is enterprise remote management.

The destination is a **team**: coding agents from different vendors, on
different machines in a tailnet, working the same problem under one policy
boundary with a durable record of who did what. cmesh gets there through the
conductor model — one orchestrating agent delegates bounded tasks, workers
return evidence, and work products move as git branches rather than as prose fed
into another agent's prompt. [docs/teams-plan.md](docs/teams-plan.md) is the
detailed plan of record; this page is the summary.

## Now

- Keep the default setup narrow: `query` first, explicit peers, explicit cwd allowlists, and no public listener.
- Preserve the least-authority profile ladder: deterministic probes before read-only agents, repo edit profiles, desktop control, or system change profiles.
- Keep built-in high-risk profiles explicit and lease-gated; require custom high-risk profiles to opt into the same `require_lease` guard.
- Keep CI focused on reproducible Go tests, workflow linting, vulnerability scanning, supply-chain checks, and CodeQL.
- **Coherence cleanup** (teams plan, Phase 0): retire the unused REST tool path in favor of the JSON-RPC MCP surface, consolidate the two harness-registration tools, and default an unspecified profile to `query` rather than the legacy `dev`.
- **Progress events** (Phase 1): expose the existing `task_events` record as a pollable tool so a conductor can narrate real progress instead of polling terminal state.

## Next

- **Task channel, worker to origin** (Phase 2a): a durable, audited, per-task message log so a worker can post progress and ask a question — including a typed, policy-checked approval request — without the task having to die to do it. Adds an `awaiting_input` state. Needs no harness streaming, so it works for every MCP-capable runner immediately.
- **Cross-peer handoff** (Phase 3): seed a task on one peer from another peer's branch and bundle, with lineage recorded in the task record, the audit event, and the commit footer. This is the core team motion — implement on one machine, verify natively on another.
- Add OS sandbox wrappers where practical:
  - macOS Seatbelt profile support for runner subprocesses.
  - Linux systemd sandboxing examples for service deployments.
  - Windows restricted user or job object guidance for worker runners.
- Add tamper-evident audit logs with hash chaining or signatures.
- Expand local smoke coverage for daemon-to-daemon flows that can run without a live tailnet.
- Add signed runner manifest verification so local config is not the only trust root for runner claims.
- Improve helper tooling for origin-side result verification, including optional diff review and command receipt checks.

## Later

- **Inbound live steering** (Phase 2b): keep stdin open on a running task so the origin can redirect an agent mid-flight, via each harness's streaming mode. Runners that cannot stream degrade to checkpoint delivery rather than losing the feature.
- **Workstreams** (Phase 4): name the delegation thread that `origin_session_id` already represents, filter tasks by it, and report status across every peer in one call.
- **Generic runner adapter** (Phase 5): a config-driven runner contract so any CLI coding agent can join the mesh without a bespoke adapter, with honesty enforced by behavioral doctor checks rather than declaration.
- Add a shared-team authorization layer for per-human approval and role separation.
- Add richer policy checks for prompt-content risk before a high-risk profile is used.
- Add more typed probes so common tasks do not need shell or agent-backed profiles.

## Intentionally out of scope

- Public internet exposure of the mesh RPC endpoint.
- Treating worker output as trusted truth.
- Silent privilege escalation from a low-risk profile to a high-risk profile.
- Replacing Tailscale account security, OS account isolation, filesystem permissions, or human review.
- **Free-form agent-to-agent chat.** Workers never message each other. Letting one agent's output become another agent's instructions is a prompt-injection amplifier and destroys the audit story; handoff moves branches and refs instead.
- **Cross-node runner session resumption.** There is no shared session across machines. The portable state is the git branch, not a harness's hidden memory.
