# MCP onboarding tools

cmesh setup happens by talking to your AI coding agent, which calls cmesh MCP tools
on your behalf. You normally never invoke these directly — you say "help me setup
cmesh" and the wizard drives them. This page is the reference for what those tools
are, so you can follow what the agent is doing, debug a stuck setup, or drive a step
by hand.

For the narrative walkthrough, see [Quickstart](quickstart.md). For the design, see
[Architecture](architecture.md).

## Two MCP surfaces

cmesh exposes two distinct MCP servers, split by whether the daemon is running:

| Surface | Transport | When | Purpose |
|---|---|---|---|
| **Setup MCP** | stdio (`cmesh mcp --stdio`) | before the daemon exists | first-run onboarding |
| **Gateway MCP** | HTTP loopback (`http://127.0.0.1:7777/mcp`, served by `cmesh up`) | after the daemon is up | task delegation |

`cmesh install` registers the **setup MCP** into your agent. The wizard runs there
until the daemon is healthy, then `register_gateway` switches your agent's cmesh MCP
over to the **gateway MCP**. A single end-of-flow session restart loads the new one.

The remote mesh RPC surface (`:7778`) is never an MCP endpoint — the only MCP your
agent talks to is local loopback.

## The driver: `onboarding_status`

`onboarding_status` is the entrypoint. **Call it first and follow `next_step`.** It is
a stateless wizard: it re-derives where setup stands from the live environment on
every call (verify, don't ask), so it is safe to call repeatedly, safe to resume
after a stop, and safe to run again on a second machine to continue forming the mesh.

It returns the current `state`, a human `next_step`, and the detected machine and
peers. The states form the onboarding path:

- `needs_config` → call `configure_node`
- `daemon_down` → call `start_service`
- `no_cmesh_peer` / `peer_untrusted` → bring up another node, then `link_peer`
- `peer_policy_denied` → fix the named policy on the other node
- `ready` → call `register_gateway`

Each step below is consent-gated: the wizard only stops to ask permission to *change*
something (write config, start the service, trust a peer), never to confirm a fact it
can detect itself.

## Setup tools (stdio surface)

These run before and during daemon bring-up.

### Read-only / detection

- **`onboarding_status`** — the driver above. Start here.
- **`doctor`** — check local setup readiness before the daemon is running.
- **`init_status`** — first-run status: detected runners, local Tailscale identity, cmesh-capable tailnet peers.
- **`detect_runners`** — detect supported local runners (Claude Code, Codex).
- **`list_tailnet_peers`** — list Tailscale peers and probe which expose cmesh.
- **`suggest_config`** — generate an opinionated config TOML from setup intent. Does **not** write files.
- **`validate_config`** — validate a config file or TOML string without starting the daemon.
- **`gateway_info`** — show the local gateway URL and daemon run command.

### State-changing (each requires `confirmed=true`)

- **`configure_node`** — write this node's `~/.config/cmesh/config.toml`, auto-filled from detection (role, safe cwds, discovered cmesh-capable peers). Preserves an existing `gateway.host_token` on re-run so it never locks you out of an already-registered gateway. Shows a preview first; writes only on `confirmed=true`.
- **`start_service`** — install and start the cmesh background service (launchd / systemd user service / Windows service), then poll the local gateway `/health` until it is up.
- **`link_peer`** — trust and reach one discovered cmesh peer: writes the reciprocal `[peers.X]` block and the outbound allow (and, on a worker/`both` node, the inbound allow), keyed on the peer's **whois-matchable identity**, then runs a bidirectional smoke. The identity is resolved from the peer's own `/v1/hello` (`node_name` + `stable_id`) and the tailnet DNS label — never a human display name — so the allowlist matches what Tailscale `whois` reports for the connection. Bounded, single-peer grant; never a wildcard.
- **`register_gateway`** — point your agent's cmesh MCP at the local HTTP gateway (Claude Code or Codex). Triggers the single end-of-flow session restart. The host token is never echoed.

## `repair_peers` (both surfaces)

Available on **both** the setup MCP and the gateway MCP, because a peer entry can go
wrong long after onboarding. Use it when a peer stops authenticating
(`peer_unauthorized`) or a `[peers.*]` entry looks wrong.

It checks every configured peer against its authoritative **tailscale whois**
identity (the trust anchor the inbound policy actually matches against), and returns a
per-peer diff with one of four statuses:

- `ok` — the entry already matches the peer's whois identity.
- `fixable` — the stored identity or allowlist is stale; it can be rewritten.
- `conflict` — the peer's self-reported `/v1/hello` name disagrees with its whois identity. **Left unchanged** for you to review; this can signal an address takeover or misconfiguration.
- `unreachable` — no live tailnet peer at that address; cannot verify, so left unchanged.

Called without `confirmed=true`, it only reports. With `confirmed=true`, it rewrites
the `fixable` entries to the whois-matchable identity, prunes stale duplicate
`[peers.*]` sections for the same machine, and reconciles the allowlists — while
**preserving trust posture** (it never grants a peer more access than it already had,
and an origin-only node never gains an inbound grant from a repair).

Security note: a repair never writes a peer's self-reported `/v1/hello` name into an
allowlist — that string is attacker-influenceable. It anchors on `whois` and surfaces
any disagreement as a conflict rather than silently rewriting.

When invoked on the **gateway** surface, the gateway is the running daemon, which
loads its policy once at startup and has no live config-reload path — so a repair
rewrites the config file but **the daemon must be restarted** for the change to take
effect. The tool's response says so explicitly; restart with
`cmesh up service restart` (or stop and re-run `cmesh up`).

## Driving a step by hand

The tools are plain MCP `tools/call` invocations. If the wizard is stuck, you can ask
your agent to call a specific one — for example, "call cmesh `onboarding_status` and
show me the result", or "call `repair_peers` without confirming and show the diff".
State-changing tools refuse to act until you pass `confirmed=true`, so a read-only
probe is always safe.
