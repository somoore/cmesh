This is a big, real improvement. You did not just add profile names; you added the beginning of a real permission system:

* internal/profile with built-in templates and semantic validation.
* query, query-network, query-shell, readonly-agent, repo-dev, desktop-control, system-change.
* ProbeRunner for deterministic query tasks.
* ShellRunner for allowlisted diagnostics.
* query_peer, inspect_peer, profile_guide, profile_recommend, profile_generate, profile_validate, profile_configure, profile_use, profile_current, profile_reset.
* Active profile context in profile-context.json.
* Query-first onboarding language in setup MCP.
* Generated configs defaulting toward query.
* Claude/Codex runner config now partially reflects profile constraints.
* Smoke test handles query via system_info probe.

That is the correct direction. The project is moving from “remote prompt runner” toward policy-shaped endpoint tasking, which is exactly where it needs to go.

I still could not run tests because go.mod requires go 1.26.4, and the sandbox cannot download the toolchain or dependencies. So this is a static review.

My overall verdict

You are very close to having the right foundation, but I would not claim “query/read-only is enforced” publicly yet. I would say:

query via ProbeRunner is moving toward hard enforcement.
query-shell and readonly-agent are still softer and need clearer boundaries.

The biggest win is that query now has a real non-agent execution path. That is exactly what we wanted.

The biggest remaining issue is that “lease” is currently more of a local active context marker than a worker-verified authorization object.

What I really like

1. query is no longer model-obedience based

This is the most important improvement.

ProbeRunner does not call Claude/Codex. It handles typed queries like:

* system_info
* disk_info
* repo_status
* network_check

That means query can eventually be described as:

no agent shell, no writes, no arbitrary commands.

That is the right safe default.

2. Built-in profile catalog is the right abstraction

The templates in internal/profile/profile.go are exactly the right product model. You now have named capability bundles instead of vague modes.

This is the right taxonomy:

query
query-network
query-shell
readonly-agent
repo-dev
desktop-control
system-change

That gives users flexibility without collapsing everything into readonly/dev/yolo.

3. MCP onboarding is becoming a guided safety wizard

The setup prompt now tells the model to use profile_guide / profile_recommend, recommend least power first, and require acknowledgements for higher-risk profiles. That is good.

This is exactly how you make the MCP setup flow valuable: the agent becomes the guided installer, not the policy authority.

4. Active profile context is a good UX primitive

profile_use, profile_current, and profile_reset are the right shape. The user should be able to say:

cmesh profile use win11 query

or:

cmesh profile use win11 repo-dev --cwd ... --ttl 30m

Then natural tasks inherit that context.

That matches your desired UX.

5. You added semantic validation

ValidateSemantics now rejects obvious contradictions:

* writes=deny + edit_repo
* shell=deny + shell_readonly
* probe + shell/edit/system capabilities
* high-risk capabilities without explicit selection
* high enforcement confidence on non-probe runners

That is exactly the right direction.

P0 issues I would fix next

1. require_lease is not a real remote lease yet

This is the biggest issue.

Right now, profile_use writes a local active context with a generated lease_id. Then the origin sends profile_context_id. On the worker side, CheckInbound mostly checks:

if profile.RequireLease && strings.TrimSpace(intent.ProfileContextID) == "" {
    deny
}

That means a peer can satisfy require_lease by sending any non-empty profile_context_id.

That is not a real lease. It is a string.

For personal dogfooding, this is okay as a local UX marker. For actual security, it is not enough.

You need one of these designs:

Option A: worker-side lease store

The worker owns leases.

Flow:

origin asks for repo-dev lease
worker creates lease_id with peer/profile/cwd/ttl
worker stores it locally
origin includes lease_id in future tasks
worker validates lease_id against store

Lease must bind:

* origin identity from whois;
* profile;
* cwd scope;
* expiry;
* optional capabilities;
* maybe origin node ID / stable ID.

Option B: signed lease token

Worker issues a signed token. Origin stores it. Worker verifies signature and claims on every task.

This avoids a DB lookup but adds key management.

For now, I’d do worker-side lease store. Simpler and auditable.

Until then, rename the current behavior mentally as:

active profile context

not:

lease enforcement

2. query_peer should not silently fall back to readonly-agent

This path is risky:

query_peer unsupported kind
  → fallbackInspectForUnsupportedQuery
  → inspect_peer
  → readonly-agent

That means a typed safe query can silently become an agent-backed read-only task if readonly-agent exists.

I understand why you added it for UX, but for security posture I would change it.

Better behavior:

* query_peer only runs typed query capabilities.
* If kind unsupported, return capability_denied or profile_denied.
* Response may suggest: “Use inspect_peer with readonly-agent if you want open-ended inspection.”

Do not auto-escalate from deterministic probe to agent runner.

This matters because query_peer should mean:

typed, bounded, enforced.

Not:

maybe typed, maybe an agent prompt.

3. readonly-agent currently allows shell

The built-in readonly-agent template has:

Writes: deny
Shell: allow
Runner: agent-backed

And readOnlyInspectionPrompt says “do not write/delete/change system state.”

That is not strong read-only. It is prompt-constrained agent execution with shell available.

I would split this:

readonly-agent
  shell = deny
  capabilities = read_files / summarize_bounded_input
readonly-agent-shell
  shell = allowlisted or harness-limited
  enforcement = medium-low / loud audit

Or make readonly-agent default to shell denied and push shell diagnostics into query-shell.

Right now, readonly-agent is likely to be misunderstood as “safe read-only,” when it is actually “agent read-only by instruction/harness controls.”

4. Query idempotency will return stale results

queryPeerCall creates a deterministic idempotency key when none is provided:

query + peer + kind + cwd + profile + params

That means asking “how much disk is free?” twice may replay the old task instead of running a fresh query.

For query tasks, that is bad. Queries are about live state.

Change this:

* If caller provides idempotency_key, use it.
* If no key, generate a fresh task ID/request.
* For query_peer, do not default to deterministic replay.

Idempotency protects retries. It should not become caching.

5. query_peer ignores active profile context

delegate resolves active profile context when profile is empty. query_peer currently defaults to query directly:

profileName := firstNonEmpty(req.Profile, profiledefs.Query)

That means if the user did:

cmesh profile use win11 query-shell --ttl 30m

then calls query_peer without a profile, it still starts at query and may route to query-shell only for known mapped capabilities.

I’d make query_peer resolve active profile context too, but with guardrails:

if explicit profile given:
  use explicit profile
else:
  use active profile for peer
else:
  query

Then if active profile does not grant the requested kind, return a clear error or recommend the right tool/profile.

6. query-shell needs command-specific argument validation

The shell runner is a good idea, but currently it mostly validates:

* command name is allowlisted;
* denied patterns are absent;
* args are strings.

That is not enough long term. An allowlisted command with arbitrary args can still do surprising things.

Example risks:

* journalctl can read lots of sensitive logs.
* Get-EventLog can expose sensitive local event data.
* powershell -Command Get-Service <args> argument behavior can still be weird.
* allowlisting command name alone is not the same as allowlisting operation shape.

The next version should have per-command validators:

Get-Service:
  allowed args: -Name, -DisplayName
  denied args: anything invoking script blocks / output redirection
journalctl:
  allowed args: -u, -n, --since
  max lines required
log:
  max duration / predicate constraints

Keep query-shell medium confidence and loud audit until then.

7. disk_info path/volume params need stricter validation

disk_info accepts path or volume.

That is useful, but for a query profile I’d validate:

* no UNC paths on Windows unless network capability is present;
* no remote/network mounts unless explicitly allowed;
* no arbitrary sensitive path probing if you care about path existence leakage;
* platform-specific volume syntax.

For disk info, it is probably okay to ask about C: or /, but the runner should make that explicit.

Suggested next implementation sequence

Commit 1: Make query fresh by default

Change query idempotency:

query_peer without idempotency_key creates a fresh task

Only deterministic if explicit key provided.

Commit 2: Remove automatic fallback from query_peer to inspect_peer

Make query_peer typed-only.

Return a helpful error:

kind "battery_status" is not a typed query capability under profile query.
Use inspect_peer with readonly-agent, or configure a typed probe.

Commit 3: Make active profile apply to query_peer

Resolve active profile context in query_peer the same way as delegate.

Commit 4: Make readonly-agent shell-deny by default

Move shell-capable read diagnostics to query-shell.

If you still want agent + shell, create readonly-agent-shell or make it custom/advanced.

Commit 5: Implement real worker-side leases

Add mesh RPC endpoints or local worker API:

POST /v1/profile-leases
DELETE /v1/profile-leases/{id}
GET /v1/profile-leases

Lease fields:

{
  "lease_id": "lease_...",
  "origin_node_id": "...",
  "origin_stable_id": "...",
  "profile": "repo-dev",
  "cwd": "C:\\Users\\scott\\code\\cmesh",
  "capabilities": ["read_files", "edit_repo", "execute_in_repo"],
  "expires_at": "..."
}

Then CheckInbound validates the lease.

Commit 6: Add parameter validators for probes/shell

Especially:

* disk path/volume;
* network host/port;
* shell command args;
* log tail line limits.

Commit 7: Add profile enforcement tests

The key tests:

query cannot run shell
query cannot use readonly-agent fallback
query cannot change wallpaper
query cannot edit repo
query-network required for network_check
query-shell denies non-allowlisted command
query-shell denies allowlisted command with unsafe args
repo-dev requires real worker-side lease
fake profile_context_id does not satisfy require_lease

One design note: query-shell should be optional, not part of the default happy path

Default first-run should still be:

query only

Then:

query-network optional
query-shell optional, advanced
readonly-agent optional
repo-dev explicit + TTL
desktop/system scary

The more I think about it, the strongest OSS trust story is:

Fresh install gives deterministic query probes only. No shell. No writes. No agent execution.

That is very defensible.

My updated read on the project

This is no longer just a prototype. You are building the permission architecture now.

The current branch is a strong step because it introduces the right concepts. The remaining work is making sure the concepts are not just labels:

* query must mean deterministic and fresh.
* readonly-agent must not pretend to be hard read-only.
* require_lease must be worker-verified.
* query_peer must not silently escalate to an agent runner.
* query-shell must validate operation shape, not just command name.

Fix those, and you will have a genuinely credible safe default.

Bottom line

This is a very good update. The architecture is now pointing the right way.

My honest recommendation:

Freeze new features and harden the profile boundary.

Specifically, make query boringly safe and make every escape hatch explicit. Once that is true, your onboarding story becomes excellent:

Install cmesh.
Open Claude/Codex.
Run setup wizard.
Default profile: query.
Ask peers live questions.
No writes. No shell. No arbitrary agent.
Everything audited.

That is the foundation people will trust.