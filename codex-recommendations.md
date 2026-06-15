# cmesh Recommendations — Profile / Permission Hardening
## Summary
The current codebase has moved in the right direction. `query` is now a real first-class profile backed by `ProbeRunner`, not just a prompt instruction. That is the correct security foundation.
The remaining work should focus on preventing accidental elevation, tightening lease authorization, improving visibility into active authority, and expanding deterministic probes so users do not need to escalate to shell/agent profiles as often.
Do **not** add heavy enterprise policy machinery yet. The right model is:
> Safe by default, explicit elevation, TTL-based convenience, everything audited.
---
## Current Good State
The following pieces are already in place and should be preserved:
- `query` profile exists and is backed by `ProbeRunner`.
- `query` denies shell and writes.
- `query_peer` exists as a first-class MCP tool.
- `query_peer` no longer falls back to `inspect_peer`.
- `query_peer` creates fresh tasks by default.
- Active profile context exists.
- `readonly-agent` is shell-deny by default.
- `repo-dev`, `desktop-control`, and `system-change` are treated as elevated profiles.
- Worker-issued leases exist.
- Profile templates exist:
  - `query`
  - `query-network`
  - `query-shell`
  - `readonly-agent`
  - `repo-dev`
  - `desktop-control`
  - `system-change`
- MCP onboarding can now guide users through profile setup.
---
# P0 Recommendations
## 1. Scope Lease List/Revoke Authorization
### Problem
The worker now has real profile leases, which is good. However, lease listing and revocation need strict ownership checks.
A remote peer should not be able to list or revoke leases issued to other peers.
### Required Behavior
Remote peers:
- can list only leases issued to themselves;
- can revoke only leases issued to themselves;
- cannot see other peers’ leases;
- cannot revoke other peers’ leases.
Local operator/admin:
- can list all leases;
- can revoke any lease.
Every list/revoke action should be audited.
### Suggested API Shape
```go
ListForPeer(identity)
RevokeForPeer(identity, leaseID)
ListAllForLocalOperator()
RevokeAnyForLocalOperator(leaseID)

Acceptance Tests

peer A cannot list peer B's leases
peer A cannot revoke peer B's lease
peer A can list its own leases
peer A can revoke its own lease
local operator can list all leases
local operator can revoke any lease
lease revoke audit includes caller identity

⸻

2. Do Not Auto-Route Into Explicit or Lease-Required Profiles

Problem

query_peer can resolve an active profile and then route to another typed profile if the requested capability is not available in the active one.

That is useful, but dangerous if the destination profile has:

RequireExplicit = true
RequireLease = true

Example:

active profile: query
requested query: network_check
available profile: query-network

If query-network requires explicit activation, cmesh should not silently switch into it.

Required Behavior

Auto-routing is allowed only when the candidate profile:

* does not require explicit activation;
* does not require a lease;
* is same-risk or lower-risk than the active profile.

For now, the simplest safe rule is:

Never auto-route into RequireExplicit or RequireLease profiles.

Better User Experience

Instead of silent escalation, return:

network_check requires profile query-network.
Enable it temporarily with:
  cmesh profile use win11-desktop query-network --ttl 15m
Nothing was executed.

Acceptance Tests

active query + disk_info => allowed
active query + network_check + query-network RequireExplicit => denied with guidance
active query-network + network_check => allowed
active query + service_status + query-shell RequireExplicit => denied with guidance
active query-shell + service_status => allowed
active query + repo edit => denied with repo-dev guidance

⸻

3. Make Active Authority Highly Visible

Problem

Users should not have to specify the profile in every prompt. However, they need to know what authority is currently active.

The goal is not constant confirmation. The goal is ambient visibility.

Recommended CLI Output

cmesh profile current:

Default profile: query
Peer             Active profile      Scope          Expires     Risk
win11-desktop    query               default        -           low
homelab          repo-dev            worker_lease   24m         high
macbook          query-shell         session        8m          medium

cmesh status should include the same profile summary.

MCP list_peers() Should Include

{
  "peer": "win11-desktop",
  "active_profile": "query",
  "profile_source": "default",
  "elevated": false,
  "risk": "low"
}

For elevated profiles:

{
  "peer": "homelab",
  "active_profile": "repo-dev",
  "profile_source": "worker_lease",
  "elevated": true,
  "expires_in_s": 1440,
  "cwd_scope": "/home/scott/code/cmesh",
  "risk": "high"
}

Acceptance Tests

profile_current shows default query
profile_current shows active profile per peer
profile_current shows lease expiration
profile_current marks elevated profiles
list_peers exposes active profile metadata
status shows current profile state

⸻

4. Add Global Safe Reset

Problem

Users need a quick way to get back to a known-safe state.

This reduces anxiety around using elevated profiles.

Recommended Command

cmesh profile reset --all

Expected behavior:

All active profile contexts reset to query.
Revoked 2 elevated worker leases.

If some remote lease revocations fail:

All local active profile contexts reset to query.
Warning: failed to revoke 1 remote lease. It expires in 7m.

Acceptance Tests

reset --all resets all local active profiles to query
reset --all revokes owned elevated leases where possible
reset --all does not revoke leases owned by other peers
reset --all reports failed remote revocation
reset --all writes audit events

⸻

P1 Recommendations

5. Add More Deterministic Probes

Why

The more useful query is, the less often users need to escalate to query-shell, readonly-agent, or repo-dev.

This keeps cmesh useful while preserving the safe default.

Recommended Next Probes

memory_info
uptime
service_status
process_list
port_check
log_tail_bounded

Suggested Capability Mapping

disk_info          query
system_info        query
memory_info        query
uptime             query
repo_status        query
service_status     query or query-shell, depending on implementation
process_list       query
port_check         query-network
network_check      query-network
log_tail_bounded   query or query-shell, but tightly scoped

Important Notes

log_tail_bounded can leak secrets. It should require:

* explicit path allowlist or service selector;
* max lines;
* max bytes;
* no full-file reads by default;
* audit level loud.

Acceptance Tests

memory_info returns structured output
uptime returns structured output
service_status returns structured output
process_list is bounded
port_check requires query-network
log_tail_bounded enforces max lines
log_tail_bounded enforces max bytes
log_tail_bounded denies arbitrary paths
all probes produce audit events

⸻

6. Add Structured Query Result Field

Problem

Probe output currently works, but long-term the result envelope should expose structured query results directly so agents do not have to parse stdout.

Recommended Result Shape

{
  "status": "done",
  "task_kind": "query",
  "query_result": {
    "kind": "disk_info",
    "data": {
      "volume": "C:",
      "total_gb": 99.02,
      "free_gb": 48.70,
      "used_gb": 50.33,
      "percent_free": 49.2
    }
  },
  "worker_summary_untrusted": "C: has 48.70 GB free out of 99.02 GB.",
  "verification_required": false
}

Acceptance Tests

disk_info result includes query_result.kind
disk_info result includes query_result.data
query_result does not require parsing stdout
task_result still includes bounded human summary
query_result appears in MCP response
query_result appears in audit/result record

⸻

7. Add Profile Risk Levels

Why

Risk levels make the UI and MCP guidance cleaner without requiring complex policy.

Suggested Risk Levels

low       query
medium    query-network, query-shell, readonly-agent
high      repo-dev, desktop-control, system-change

Example

[profiles.query]
risk = "low"
[profiles.query-shell]
risk = "medium"
[profiles.repo-dev]
risk = "high"

Rules

* Low-risk profiles can be active by default.
* Medium-risk profiles require explicit activation and TTL.
* High-risk profiles require explicit activation, TTL, and worker-issued lease.
* desktop-control and system-change should use loud audit.

Acceptance Tests

profile templates expose risk
status displays risk
MCP list_peers displays risk
high-risk profiles require explicit activation
high-risk profiles require lease
medium-risk profiles require explicit activation
low-risk query is default

⸻

P2 Recommendations

8. Add Lease Scope Modes

Problem

Current cwd lease matching appears exact-only. That is safe but may be awkward for repo workflows.

Recommendation

Keep exact as default, but support explicit recursive scope.

cwd_scope = "exact"
cwd_scope = "recursive"

Rules

* exact means only the exact normalized cwd is allowed.
* recursive means cwd and children are allowed.
* recursive must be explicit.
* all cwd comparisons happen after realpath normalization.

Acceptance Tests

exact lease allows exact cwd
exact lease denies child cwd
recursive lease allows child cwd
recursive lease denies sibling cwd
symlink escape denied
case-insensitive bypass denied on Windows/macOS

⸻

9. Add Better Denial Guidance

Problem

Security denials should be helpful, not frustrating.

Recommended Denial Format

{
  "error": "profile_denied",
  "detail": "network_check requires query-network, but active profile is query.",
  "nothing_executed": true,
  "suggested_command": "cmesh profile use win11-desktop query-network --ttl 15m"
}

Examples

For repo edit under query:

Denied: active profile is query.
This task appears to require repo-dev.
To allow repo-local edits for 30 minutes:
  cmesh profile use win11-desktop repo-dev --cwd C:\Users\scott\code\cmesh --ttl 30m
Nothing was executed.

For desktop control:

Denied: desktop-control is not active.
This profile can change the remote desktop/session.
To allow it temporarily:
  cmesh profile use win11-desktop desktop-control --ttl 10m
Nothing was executed.

Acceptance Tests

denial includes error code
denial includes human-readable detail
denial includes nothing_executed=true
denial includes suggested command when possible
denial does not suggest unsafe broad profiles

⸻

10. Improve doctor Around Profiles and Runners

Recommended Commands

cmesh doctor profile query
cmesh doctor profile query-shell
cmesh doctor runner probe
cmesh doctor runner claude-code
cmesh doctor service-context

Useful Checks

For query:

profile exists
runner = probe
writes = deny
shell = deny
network = false
capabilities valid
probe runner available

For query-shell:

profile exists
runner = shell
shell policy present
allowed commands valid for OS
denied patterns present
max output set
max runtime set
audit level loud

For service context:

service user
home directory
PATH
runner path
runner auth
Tailscale LocalAPI access
cwd access
git available

Acceptance Tests

doctor query passes valid profile
doctor query fails if shell enabled
doctor query fails if writes enabled
doctor query-shell warns on missing validators
doctor service-context reports service user
doctor runner claude-code checks stdin smoke

⸻

Things Not To Add Yet

Avoid adding heavy systems until the core experience is boring.

Do not add yet:

OPA/Cedar
enterprise RBAC
approval queues
central admin dashboard
multi-tenant federation
signed policy bundles
complex delegation chains
per-command interactive prompts
SaaS control plane

These will increase friction and slow the project down.

The current best product shape is:

query by default
explicit temporary elevation
TTL leases
clear status
helpful denials
audit everything

⸻

Recommended Next Commit Order

Commit 1

Lease ownership authorization

* Scope list/revoke to owning peer.
* Local operator can list/revoke all.
* Add tests.

Commit 2

No auto-routing to explicit/leased profiles

* Refuse silent escalation to RequireExplicit or RequireLease.
* Return helpful guidance.
* Add tests.

Commit 3

Profile visibility

* Improve profile_current.
* Add active profile/risk/expiry to cmesh status.
* Add active profile/risk/expiry to MCP list_peers.

Commit 4

Global safe reset

* Add cmesh profile reset --all.
* Revoke owned leases where possible.
* Add audit events.

Commit 5

Structured query result

* Add query_result to result envelope or runner_extra.
* Update ProbeRunner.
* Update MCP result formatting.

Commit 6

More probes

Add:

* memory_info
* uptime
* service_status
* bounded process_list

Keep port_check under query-network.

Commit 7

Doctor improvements

Add:

* doctor profile
* doctor runner probe
* doctor service-context

⸻

Final Recommendation

Do not add more friction. Add visibility, temporary elevation, and safe defaults.

The ideal user experience should be:

Default: query
No shell. No writes. No arbitrary agent.
Ask useful live questions immediately.
Need more power?
Activate a profile once with TTL.
Work naturally.
It expires back to query.
Everything is audited.

That preserves the magic while preventing the obvious security nightmare.