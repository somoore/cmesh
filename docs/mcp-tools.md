# MCP tools

> Experimental: cmesh can cause real changes on real machines. Do not use it on anything you love: machines, repos, credentials, or data you cannot afford to lose. Treat every worker profile as real authority on the worker machine, and prefer the `query` profile.

cmesh works by talking to your AI coding agent, which calls cmesh MCP tools on your behalf. You normally never invoke these directly. This page is the complete reference for what the tools are, so you can follow what the agent is doing, debug a stuck flow, or drive a step by hand.

For the setup narrative, see [Quickstart](quickstart.md). For the onboarding flow specifically — the wizard state machine and the order tools are called in — see [MCP onboarding tools](mcp-onboarding.md). For the design, see [Architecture](architecture.md).

## Two MCP surfaces

cmesh exposes two distinct MCP servers, split by whether the daemon is running:

| Surface | Transport | When | Purpose |
|---|---|---|---|
| **Setup MCP** | stdio (`cmesh mcp --stdio`) | before the daemon exists | first-run onboarding |
| **Gateway MCP** | HTTP loopback (`http://127.0.0.1:7777/mcp`, served by `cmesh up`) | after the daemon is up | task delegation and operation |

`cmesh install` registers the **setup MCP** into your agent. The wizard runs there until the daemon is healthy, then `register_gateway` switches your agent's cmesh MCP over to the **gateway MCP**. A single end-of-flow session restart loads the new one. The remote mesh RPC surface (`:7778`) is never an MCP endpoint — the only MCP your agent talks to is local loopback. See [Architecture](architecture.md) for how these surfaces sit in the request lifecycle.

The two servers overlap deliberately. Detection, profile inspection, config validation, and peer repair are useful both before and after the daemon exists, so those tools are present on **both** surfaces. Onboarding state-machine tools live only on the setup MCP; task delegation and per-peer profile context live only on the gateway. Each tool below is annotated with the surface(s) that expose it.

State-changing tools refuse to act until you pass `confirmed=true`, so a read-only probe is always safe.

## Setup and onboarding tools

These drive first-run setup and bring-up. They are on the **setup (stdio)** surface unless noted. For the order to call them in and the wizard states, see [MCP onboarding tools](mcp-onboarding.md).

| Tool | Surface | Purpose |
|---|---|---|
| `onboarding_status` | setup | The driver. Call it first and follow `next_step`. Re-derives setup progress from the live environment on every call, so it is safe to call repeatedly and on a second node to continue forming the mesh. No parameters. |
| `init_status` | both | First-run status: detected runners, local Tailscale identity, and cmesh-capable tailnet peers. No parameters. |
| `detect_runners` | both | Detect supported local runners (Claude Code, Codex). No parameters. |
| `list_tailnet_peers` | both | List Tailscale peer candidates and probe which ones expose cmesh. No parameters. |
| `suggest_config` | both | Generate an opinionated config TOML from setup intent. Does **not** write files. Params: `role`, `allowed_peers`, `allowed_cwds`, `peer_names`, `runner`, `profiles` (all optional). |
| `profile_preview_config` | setup | Preview config TOML for selected profile templates. Does **not** write files. Same params as `suggest_config`. |
| `validate_config` | both | Validate a config file or TOML string without starting the daemon. Params: `path`, `toml` (both optional). |
| `configure_node` | setup | Write this node's config, auto-filled from detection (role, safe cwds, discovered peers). Preserves an existing gateway host token on re-run. Params: `role`, `allowed_cwds`, `peer_names`, `runner`, `profiles`, `profile_acknowledgements`, `confirmed`. Writes only on `confirmed=true`. |
| `write_config` | setup | Write a config from a provided TOML string or generated setup intent. Requires `confirmed=true`. |
| `profile_write_config` | setup | Write a config from a provided TOML string or selected profile templates. Requires `confirmed=true`. |
| `start_service` | setup | Install and start the background service (launchd / systemd user service / Windows service), then poll the local gateway `/health` until it is up. Param: `confirmed`. Requires `confirmed=true`. |
| `link_peer` | setup | Trust and reach one discovered cmesh peer: writes the reciprocal `[peers.X]` block and the outbound allow (and, on a worker/`both` node, the inbound allow), keyed on the peer's whois-matchable identity, then runs a bidirectional smoke. Params: `peer` (required), `confirmed`. Requires `confirmed=true`. Bounded, single-peer grant; never a wildcard. |
| `gateway_info` | setup | Show the post-init local HTTP gateway URL and daemon run command. No parameters. |
| `register_gateway` | setup | Point your agent's cmesh MCP at the local HTTP gateway. Triggers the single end-of-flow session restart; the host token is never echoed. Params: `harness` (required: `claude-code` or `codex`), `confirmed`. Requires `confirmed=true`. |
| `register_harness` | gateway | Register the running local gateway with a local harness. Params: `confirmed`, `harness` (`claude-code` or `codex`; defaults to `claude-code`). Requires `confirmed=true`. Shares one implementation with the setup-side `register_gateway`, so both surfaces behave identically — including removing the stdio registration being replaced. An unrecognized harness returns manual instructions rather than failing. The gateway host token is written into harness config but never echoed: previews are redacted. |

`configure_node` always overwrites an existing config (preserving only the gateway host token), so re-running setup never locks you out of an already-registered gateway.

## Delegation and operation tools

These are on the **gateway (HTTP)** surface only — they need the running daemon. This is the surface your agent uses after setup.

### Sending work

| Tool | Purpose |
|---|---|
| `delegate` | Delegate a coding-agent task to one or more peers. Use for repo work and explicit profile workflows. Delegation hands the worker the task *itself*, not a transcript of your own work: state intent, constraints, and acceptance criteria; let the worker author the deliverable. Params: `prompt` (required), `cwd` (required), `peer` or `peers` (fan-out), `runner`, `profile`, `timeout_s`, `wait`, `allow_dirty`, `idempotency_key`, `followup_task_id`, `task_token`. Single-peer calls return a `task_id` immediately unless `wait` is true; fan-out returns a peer-keyed map. |
| `inspect_peer` | Ask an open-ended read-only question about a peer using `readonly-agent` — battery remaining, app inventory, last reboot, opening or summarizing files. Denies shell, writes, deletes, and system-change requests by profile policy and prompt contract. Params: `peer` (required), `prompt` (required), `cwd`, `profile`, `timeout_s`, `wait`, `idempotency_key`, `task_token`. |
| `query_peer` | Run a deterministic typed cmesh query through a query profile. Native kinds: `disk_info`, `system_info`, `repo_status`, plus configured probe capabilities such as `network_check`. Empty profile resolves from the active context, then defaults to `query`. For open-ended questions use `inspect_peer` instead of inventing kinds. Params: `peer` (required), `kind` (required), `params`, `cwd`, `profile`, `timeout_s`, `wait`, `idempotency_key`, `task_token`. |
| `task_catalog` | List bounded delegation recipes (disk checks, repo status, native builds, service log summaries, test runs, branch handoff). No parameters. |

### Tracking and retrieving tasks

Task operations are owner-scoped: the origin may read its tasks, and an intermediate worker may read the downstream task it directly created. Unrelated peers cannot enumerate or operate on them.

| Tool | Purpose |
|---|---|
| `list_tasks` | List recent local and delegated tasks so a fresh session can rediscover task ids. Param: `limit` (optional). |
| `task_status` | Poll delegated task state, optionally long-polling until terminal or timeout. Params: `task_id` (required), `wait`, `timeout_s`. |
| `task_result` | Fetch the bounded result envelope for a terminal task. Param: `task_id` (required). |
| `task_transcript` | Fetch bounded stdout/stderr transcript logs from the worker. Param: `task_id` (required). |
| `task_artifact` | Fetch a worker artifact (currently `git-bundle`) and save it under the local cmesh data directory. Params: `task_id` (required), `kind`. |
| `cancel` | Cancel a delegated task. Params: `task_id` (required), `reason`. |

### Peer information

| Tool | Purpose |
|---|---|
| `list_peers` | List configured cmesh peers and their static mesh addresses. No parameters. |
| `peer_info` | Fetch `/v1/hello` from a peer to inspect schema versions, runners, profiles, and capabilities. Param: `peer` (required). |
| `test_peer` | Run a semantic smoke task against a peer and verify the runner receives the prompt and returns the expected result. Params: `peer` (required), `cwd`, `profile`, `timeout_s`. |

## Profile tools

Profiles set the authority ceiling for a peer. The `query` profile is the safe default. Inspection tools are on **both** surfaces; the per-peer active-context tools (`profile_current`, `profile_use`, `profile_reset`) and `profile_describe` are **gateway** only, since active context belongs to the running daemon.

| Tool | Surface | Purpose |
|---|---|---|
| `profile_templates` | both | List built-in profile templates. The default `query` template is enforced by the builtin probe runner: no shell, no writes, no arbitrary agent execution. No parameters. |
| `profile_guide` | both | Explain every profile in plain language: exact powers, enforcement strength, safer alternatives, and why `query` is the default. No parameters. |
| `profile_recommend` | both | Recommend the least-powerful profile for a stated intent and explain consent, TTL, safer alternative, and first tool to use. Param: `intent` (required). |
| `profile_generate` | both | Generate a built-in template preview and TOML snippet without writing config. Params: `template`, `name`, `runner`, `capabilities`, `os` (all optional). |
| `profile_validate` | both | Validate one profile capability contract and reject unsafe contradictions (e.g. `writes=deny` with an edit capability). Params: `runner` (required), `name`, `capabilities`, `writes`, `shell`, `network`, `git_push`, `require_explicit`, `require_lease`, `enforcement_confidence`. A profile granting a high-risk capability must set both `require_explicit` and `require_lease`, whatever it is named. |
| `profile_configure` | both | Preview or add a built-in profile to the local config (e.g. `repo-dev` bound to `codex` for a peer). Params: `template`, `name`, `runner`, `peer`, `allowed_peers`, `force`, `confirmed`. Requires `confirmed=true` to write. On the gateway it hot-reloads; on setup stdio, restart or use the gateway MCP to reload a running daemon. |
| `profile_describe` | gateway | Describe one configured profile, including effective capabilities and lease/explicit requirements. Param: `profile` (required). |
| `profile_current` | gateway | Show the gateway's active per-peer profile context. Empty `delegate` and `query_peer` profile requests resolve from this context, then default to `query`. No parameters. |
| `profile_use` | gateway | Set the active profile for a peer. The worker still enforces its own policy and may deny the request. Params: `peer` (required), `profile` (required), `cwd`, `ttl`, `profile_acknowledgement`. `ttl` is required for profiles with `require_lease=true`. |
| `profile_reset` | gateway | Clear the active profile context for one peer, or all peers when `peer` is omitted. Param: `peer` (optional). |

## Diagnostics

| Tool | Surface | Purpose |
|---|---|---|
| `doctor` | both | Check local cmesh setup readiness: config, Tailscale identity and reachability, runners, and gateway details. On the setup surface it checks readiness before the daemon is running. No parameters. |
| `repair_peers` | both | Diagnose and fix wrong, stale, or broken `[peers.*]` entries. Use it when a peer stops authenticating (`peer_unauthorized`) or an entry looks wrong. Checks every configured peer against its authoritative Tailscale `whois` identity and returns a per-peer diff (`ok`, `fixable`, `conflict`, `unreachable`). Param: `confirmed`. Without it, only reports. With `confirmed=true`, rewrites `fixable` entries to the whois-matchable identity, prunes stale duplicates, and reconciles allowlists while preserving trust posture. It never trusts a peer's self-reported `/v1/hello` name for the allowlist; `conflict` and `unreachable` peers are left unchanged. On the gateway the running daemon reloads the repaired policy immediately; the response reports `active_immediately` and only asks for a restart if that reload failed. |

`repair_peers` is the one tool you may reach for long after onboarding. See [MCP onboarding tools](mcp-onboarding.md#repair_peers-both-surfaces) for the full per-status behavior.

## Driving a step by hand

The tools are plain MCP `tools/call` invocations. If the wizard is stuck, ask your agent to call a specific one — for example, "call cmesh `onboarding_status` and show me the result", or "call `repair_peers` without confirming and show the diff". State-changing tools refuse to act until you pass `confirmed=true`, so a read-only probe is always safe.
