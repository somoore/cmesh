# cmesh — Recommended Fixes

Prioritized from the Mac↔Windows test write-up. Ordered by what undermines the core thesis first, then security, then design polish. Issue numbers (#2–#6) map to your "Issues found and fixed" section.

---

## P0 — Correctness & trust

### 1. Make task results verifiable artifacts, not model prose

**Problem.** The smoke path has a sentinel (`cmesh smoke ok`) you can verify. Real tasks don't. The disk-space result comes back as `worker_summary_untrusted` — a model's prose summary — with nothing proving Windows actually ran a command, or that `48.70 GB free` is real rather than a plausible confabulation. You named the field `untrusted` correctly; the problem is nothing downstream can ever upgrade that trust.

**Fix.**
- Define a result contract: the runner returns the raw command output (stdout + stderr + exit code) as a structured field alongside any model summary.
- Validate the artifact before the origin acts on it — schema check, exit-code check, optionally re-parse the raw output independently of the model's prose.
- Treat the model summary as presentation only. Never let automated downstream actions key off the prose; key off the verified artifact.

**Why.** This is the line between an operations fabric and "I asked an agent and it told me a number." Everything you'd build on top — fan-out, scheduled checks, agent-driven remediation — needs a trustworthy result envelope, not a vibe.

### 2. Add a runner preflight / `cmesh doctor` self-check

**Problem.** Three separate failures — missing `readonly` profile (#2), invalid `claude-fable-5` model (#3), service auth/PATH (#4) — all surfaced *during* a peer task, after the peer had already been advertised as reachable. The failure surface was remote and confusing instead of local and obvious.

**Fix.** Before a daemon advertises itself as a healthy peer, run a local self-test that:
- Confirms each configured profile exists and is runnable.
- Confirms the runner's model is accessible via a trivial local `claude -p` round-trip.
- Confirms runner auth and PATH resolve under the *actual* execution context (the service user, not the interactive user).
- Runs the smoke sentinel against itself locally.

Expose it as `cmesh doctor` and gate `cmesh up` on it (warn, or refuse to advertise).

**Why.** Converts a class of remote, mid-task failures into one cheap local check. #2, #3, and #4 would all have been caught at startup instead of by a confused peer.

---

## P1 — Security (load-bearing as this generalizes)

### 3. Stop trusting self-asserted provenance

**Problem.** `origin_node_id`, `delegation_chain`, and `hop_count` travel in the POST body. The worker authenticates the *immediate* caller via Tailscale whois, but the chain itself is unauthenticated client claims. A compromised or malicious peer can forge `hop_count: 0`, rewrite the chain, or erase its own hop.

**Fix.**
- Bind the cmesh `node_id` to the Tailscale identity cryptographically — a node should not be able to claim a `node_id` the tailnet identity doesn't match.
- Have each hop sign its addition to the delegation chain; the worker verifies the immediate caller's signature and that its signed view of the chain is consistent.
- Enforce `hop_count` / max-depth server-side from verified hops, not from the client-supplied integer.

**Why.** On a tailnet of machines you fully control, self-asserted provenance is tolerable. The moment you add transitive delegation, more peers, or anything multi-actor, the chain is the thing preventing confused-deputy and delegation-amplification — and unauthenticated claims won't hold the weight.

### 4. Scope the service runner's credentials

**Problem.** The Windows service now runs as `.\scott` to inherit that user's Claude auth and PATH (#4). Any inbound task within policy executes with that user's full agent credentials.

**Fix.**
- Document where those credentials live and how they're protected at rest.
- Prefer a dedicated least-privilege service principal over a human user account.
- Where feasible, scope per-task: hand the runner task-scoped credentials/permissions rather than ambient inherited auth.

**Why.** The execution context is now a standing credential on the box, reachable within policy over the mesh. That's a high-value target and worth treating as one — especially given how much of your other work assumes remote agent output is hostile by default.

---

## P2 — Design / next

### 5. Decide the fan-out result story before people ask for it

The disk-space example is the camel's nose — "ask all 12 machines" is the obvious next request. The transport handles it trivially (N POSTs); the result envelope from #1 is what decides whether fan-out stays useful or becomes twelve unverifiable summaries you can't eyeball. Build #1 with aggregation in mind: machine-keyed, schema-validated, diffable.

### 6. Optional: session continuity for the coding-handoff case

Stateless `claude -p` per task is the right default for an auditable fabric — clean envelopes, no session bleed. But the original thesis (agent-to-agent coding handoff) sometimes wants continuity across tasks. If so, use `--resume` keyed on `origin_session_id`, or move the worker to SDK streaming input. Keep ops tasks stateless; opt specific sessions into continuity rather than making it the default.

---

## Suggested order

1. **`cmesh doctor` preflight (#2)** — cheap, kills a whole failure class immediately.
2. **Result contract / verifiable artifacts (#1)** — unblocks everything above plain ops.
3. **Provenance signing + identity binding (#3)** — before transitive delegation exists in the wild.
4. **Service credential scoping (#4).**
5. **Fan-out aggregation (#5)** and **session continuity (#6)** as the use cases actually arrive.