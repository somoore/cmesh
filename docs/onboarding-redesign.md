# cmesh CLI + Install + Onboarding Redesign

Status: IMPLEMENTED on branch `onboarding-redesign` (build/vet/test/gofmt + win/linux cross-compile green).
Decisions locked: restructure CLI commands · confirm each state change · hand-rolled `x/term` multi-select

Implementation notes (as built, differing from or refining the plan below):
- `cmesh install` ensures the binary is on PATH (shared `ensureBinaryOnPath`,
  `--no-self-install` to skip) AND registers the MCP — so one command delivers
  "binary on PATH + MCP". `scripts/install.sh` auto-launches the picker on an
  interactive terminal (`--no-agent-setup` opt-out; non-TTY falls back to a hint).
- `node.role` is persisted in config so an origin-only node reaches `ready`
  without requiring the reverse worker smoke; worker/both keep the real-smoke gate.
- `link_peer` grants bounded bidirectional trust (see Trust-model amendment) and
  the confirm preview discloses both directions.
- `ready` is gated on a real, re-derived bidirectional smoke (not a config-text
  match) and emits the Part-5 payoff + routes to `register_gateway`.
- Discovery is surfaced via `list_tailnet_peers` + `onboarding_status.Peers`
  rather than a separate `discover_peers` tool.

## Goal

Remove friction from first-run. A user runs one installer, picks their AI coding
agent(s), and from then on does everything by talking to the cmesh MCP inside the
agent. The MCP becomes a stateless, resumable onboarding wizard that detects
rather than interrogates, and drives a two-node mesh into existence with the
complexity hidden behind automation.

## The two load-bearing realities the design must honor

These are not implementation details; they shape the entire UX.

### R1 — There is no cross-node session. The "handshake" is symmetric, independently-derived config.

The user's mental model is "node A pauses, I go to node B and say *continue*, B
resumes A's session." **That channel does not exist.** B is a fresh process on a
separate machine with zero shared state. B only learns it is joining a mesh from
(a) the user's prompt text and (b) discovering A on the tailnet via
`onboard.TailnetPeers(probeCmesh=true)` → `probeMesh` hitting A's `/v1/hello`.

The "continuation" is therefore re-entry into the **same stateless wizard** on B,
which derives its next step from the environment (config present? peer A
cmesh-capable? bidirectional smoke passing?). What actually links the nodes is
**two configs written independently** so they point at each other:

- **B** (the new worker) writes config with **A in `policy.allowed_peers`**
  (inbound trust), installs + starts its daemon, and must be reachable on the
  tailnet **before** A's smoke can succeed.
- **A** writes a `[peers.B]` block (address from tailnet discovery) and adds **B
  to outbound `allowed_peers`**.
- Then a **bidirectional smoke** (`test_peer` A→B, `PeerSmoke` B→A) validates the
  link.

If any doc, prompt, or tool description implies B "resumes A's session," the
mental model collapses. Everything is phrased as: *each node configures itself to
trust/reach the other; cmesh discovers the rest.*

### R2 — Onboarding (stdio MCP) and delegation (HTTP gateway) are two surfaces split by daemon start.

- Onboarding runs on the **stdio setup MCP** (`cmesh mcp --stdio`, no daemon, no
  network listener).
- Delegation runs on the **local HTTP gateway** (`127.0.0.1:7777/mcp`, daemon up
  via `cmesh up` / service).

The user's flow treats "the cmesh MCP" as one continuous thing. The wizard must
bridge the seam: write config → install + start the service → verify gateway
`/health` → register the gateway MCP into the agent. Because MCP servers load at
session start, registering the gateway is potentially a **second** "open a new
session" moment. We minimize this to **one** restart by registering BOTH the
stdio setup MCP (at install time) and, when possible, planning the gateway
registration so the user restarts once at the end. Codex gateway registration is
a known rough edge (`cmdConnect` already warns "varies by CLI version") — call it
out explicitly and fall back to printed manual instructions.

## Verified constraint: peer-identity matching (must-fix in the new wizard)

Inbound policy on B matches A using `peerIdentityCandidates` =
`{PeerName, PeerNodeID, PeerStableID, PeerTags}`, which the worker fills from
**A's Tailscale whois** (`identity.NodeName = firstLabel(DNSName)`, `identity.NodeID`,
`identity.StableID`, tags).

But `onboard.TailnetPeers` populates `PeerCandidate.NodeID` from
`tailscale status --json`'s `peer.ID` — the **ephemeral session node id** (e.g.
`n12345…`), which does **not** equal the whois `Node.ID` or `StableID`.
`PeerCandidate.Name` (hostname / first DNS label) **does** match whois `NodeName`.

Today `GenerateConfig` writes `firstNonEmpty(peer.NodeID, peer.Name)` into
`allowed_peers`, preferring the **non-matchable** id. **The new wizard must write
the hostname label** (or capture A's real whois identity directly) into
`allowed_peers`, or B will silently reject A at inbound policy.

Design rule: **discovery values are for reachability (address/port); trust
allowlists key on the stable, whois-matchable name.** Where possible, capture the
peer's authoritative identity from its `/v1/hello` (extend `HelloResponse` to
include `node_name` + `stable_id`, both already in `Config.Node` / resolver) and
write that.

---

## Part 1 — Binary install + global alias (bootstrap, outside the agent)

Distinct from MCP registration. This is the "global alias" piece.

1. **Shell installer** at `get.cmesh.dev` (already referenced in `cmdBootstrap`):
   `curl -fsSL https://get.cmesh.dev | sh`. Downloads the right
   platform binary, places it on PATH (`/usr/local/bin` or `~/.local/bin`, with
   shell-rc alias fallback), `chmod +x`, prints the version.
2. **`cmesh self-install`** subcommand for users who already have the binary:
   copies itself to a PATH dir and ensures the alias. Idempotent.
3. After binary is on PATH, the installer **auto-launches the interactive agent
   picker** (Part 2) unless `--no-agent-setup` is passed.

Deliverables: `scripts/install.sh` (or update existing), `cmd/cmesh` gets
`self-install`. Document both paths in `docs/quickstart.md`.

## Part 2 — Native interactive installer (agent picker, hand-rolled `x/term`)

Replaces `cmdMCPInstall` / `selectMCPInstallAgents`.

Flow:
1. `setupinstall.DetectAgents()` → **filter to detected only** (hide not-found).
2. If zero detected: print install hints for Claude Code / Codex, exit non-zero.
3. **Arrow-key + space multi-select** TUI (new `internal/tui` or
   `cmd/cmesh/picker.go`, built on `golang.org/x/term` raw mode):
   - ↑/↓ navigate, Space toggle, Enter confirm, q/Ctrl-C cancel.
   - Pre-checks all detected agents by default.
   - Degrades to numbered prompt when stdin/stdout is not a TTY (CI, pipes).
4. On Enter, echo: *"This will install the cmesh MCP into: Claude Code, Codex."*
   **Confirm Y/N** (decision: confirm each state change).
5. On Y, call `setupinstall.Install(agent, exe)` per selection (registers
   `cmesh mcp --stdio`). Print ok/fail per agent.
6. **Connectivity test** — honest scope: spawn `cmesh mcp --stdio` and perform
   `initialize` + `tools/list` over stdio to confirm the MCP handshakes. This
   verifies the MCP is wired; it **cannot** verify delegation (no daemon/peer
   yet). Label it "MCP wired ✓", not "connected to mesh."
7. Final message:
   > Done. Open a **new session** in your agent and run:
   > **help me setup cmesh**

Deliverables: new picker, rewrite of `cmdMCP`/`cmdMCPInstall`,
install-time stdio self-test helper.

## Part 3 — Stateless onboarding wizard (stdio MCP tool surface)

The heart of the redesign. Add to `internal/mcp/setupstdio` + `internal/onboard`.

### 3a. Deterministic entry: MCP prompt

Expose an MCP **prompt** named `setup-cmesh` (and advertise `prompts` capability)
so "help me setup cmesh" maps to a real prompt that instructs the model to call
`onboarding_status` first and follow `next_step`. Removes reliance on the model
guessing. (If a target agent ignores MCP prompts, the tool descriptions still
steer it.)

### 3b. One driving tool: `onboarding_status` → `{state, next_step, detail}`

Re-derives progress from the environment on **every call** (this is what makes it
resumable and what makes B's "continuation" work — B calls the same tool and sees
"fresh node, peer A detected, do X"). States, each with a concrete `next_step`
and human-readable narration:

| state | meaning | next_step |
|---|---|---|
| `tailscale_down` | backend not Running / <1 tailnet IP | guide: start/login Tailscale |
| `tailscale_solo` | up, but 0 online peers | "Install Tailscale on a 2nd machine, then continue" |
| `needs_config` | peers exist, no cmesh config here | call `configure_node` |
| `daemon_down` | config exists, gateway `/health` fails | call `start_service` |
| `no_cmesh_peer` | daemon up, no cmesh-capable peer | "Install cmesh on the other node; there run: *continue helping me configure cmesh for this new node*" |
| `peer_untrusted` | cmesh peer found but not linked, or reached and smoke fails without a policy signal (trust/connectivity gap) | call `link_peer` / fix allowlist |
| `peer_policy_denied` | peer reached, but it rejected the forward smoke task on its own policy (`cwd_denied`/`profile_denied`) | fix the **other** node's `allowed_cwds`/`denied_profiles`, restart its service, re-`link_peer` |
| `ready` | bidirectional smoke passes | print example prompts + profiles |

`detail` carries the verify-then-confirm narration (R3 below).

### 3c. Verify-don't-ask (decision principle baked into tools)

Convert the user's interrogation into detection:
- "Have you installed Tailscale on 2 endpoints?" → `DetectMachine` +
  `TailnetPeers`: we **know** the count. Only narrate it.
- "Is one endpoint this system?" → trivially true (we are running on it). Don't
  ask; state it.
Only prompt the **human** for **consent** (config write, service install/start,
peer trust) or when detection is genuinely ambiguous.

### 3d. State-changing tools (each gated on `confirmed=true`)

- `configure_node{role, allowed_cwds?, peers?, profiles?, confirmed}` — wraps
  `onboard.GenerateConfig` + `WriteConfig` + `ValidateConfig`. **Auto-fills**
  role/cwds/peers from detection; only the human consents. Writes
  whois-matchable peer names into `allowed_peers` (fixes the Part-0 constraint).
- `start_service{confirmed}` — installs + starts the OS service (launchd/systemd/
  Windows SC via existing `cmd/cmesh` service code, refactored into a callable
  package), then polls gateway `/health`. This replaces the user manually running
  `cmesh up`.
- `link_peer{peer, confirmed}` — writes the reciprocal `[peers.X]` + outbound
  allow entry for a discovered cmesh peer, then runs the bidirectional smoke.
- `register_gateway{harness, confirmed}` — swaps the agent's cmesh MCP from the
  stdio setup server to the HTTP gateway (`register_harness` logic), or prints
  manual steps for Codex. Triggers the single end-of-flow session restart.

All four return `requires_confirmation:true` with a preview when `confirmed` is
absent — same pattern as today's `write_config`.

### 3e. Tailnet/peer discovery (frictionless wrappers)

Reuse `TailnetPeers(probeCmesh=true)` and `probeMesh`. Surface as
`discover_peers` returning `{online_peers, cmesh_peers, this_node}` with plain
narration. No new transport.

## Trust-model amendment (review finding, conscious change)

Original security guidance held that `link_peer` must add the consented peer to
**outbound** allow only and **must not widen inbound** `allowed_peers`. That letter
is wrong: in the canonical sequential flow Node A is configured before Node B
exists, so A's inbound `allowed_peers` falls back to `[self]`, and nothing ever
adds B — the bidirectional smoke's reverse leg (B→A) then fails and the handshake
never closes.

Amended rule: **`link_peer` grants the single consented peer trust in BOTH
directions** — outbound (A may send work to B) and inbound (A may accept work from
B) — because linking a peer is exactly an explicit, consented, single-peer trust
grant. The *spirit* of the original guard (never broaden beyond the one consented
peer, never wildcard) is preserved:

- The inbound entry is the one peer's whois-matchable identity, glob-escaped (no
  `* ? [ ]`), never a wildcard, never any other node.
- The grant is bounded: B is still constrained by A's `allowed_cwds` and profile
  ceilings; inbound trust is "may ask," not "may do anything."
- The confirmation preview MUST disclose both directions ("A will send work to B
  AND accept work from B"), or the consent is not informed.
- The grant is role-aware: inbound trust + requiring the B→A smoke for `ready`
  only applies when this node serves as a worker (`role` = worker|both, the
  default). An origin-only node does not take the inbound grant.

## Part 4 — Two-node trust handshake (the symmetric dance)

Concrete ordering the wizard enforces (per R1). Node A = origin already set up,
Node B = the new node.

On **B** (`continue helping me configure cmesh for this new node`):
1. `onboarding_status` → detects fresh node + online peer A.
2. `configure_node{role: worker|both, confirmed}` — writes A into B's
   `allowed_peers` using A's **hostname/whois name** (captured from A's
   `/v1/hello`, see Part 0). Sets `allowed_cwds` from `SuggestedCWDs`.
3. `start_service{confirmed}` — B's daemon comes up and is reachable.
4. `onboarding_status` → `no_cmesh_peer` resolves; tells the user to go finish on
   A (or, if A is also mid-wizard, A's next `onboarding_status` now sees B).

On **A**:
5. `link_peer{peer: B, confirmed}` — writes `[peers.B]` (address from discovery) +
   B in outbound allow, runs `test_peer` A→B.
6. Bidirectional smoke: A→B via `test_peer`, B→A via `PeerSmoke`
   (`peers test … --bidirectional` already implements both directions).
7. Both reach `ready`.

Identity capture detail: extend `HelloResponse` with `node_name` + `stable_id`
(already available from config/resolver) so each side records the **authoritative
matchable identity** of the other, not a discovery-only ephemeral id.

## Part 5 — Post-onboarding (the payoff)

When `ready`, the wizard prints:
- **Per-peer default profile** (read from each side's config:
  `runners.<r>.default_profile`, now `query`) and how to switch
  (`profile: "query"` or `profile: "repo-dev"` in a delegate call; the
  profile menu: query/query-shell/readonly-agent/repo-dev/network/interactive/
  git_push, with the security note that agent-backed and elevated profiles are
  real authority).
- **Example prompts**, drawn from `taskRecipes()` (disk check, repo status,
  native build, service logs, test runner, branch handoff), templated with the
  real peer name:
  > Use cmesh to ask **B** to report disk usage for / (query).
  > Use cmesh to ask **B** to run the test suite in ~/code/myapp (repo-dev).

## Part 6 — CLI restructure + migration (decision: restructure commands)

New top-level surface (small, intentional):

```
cmesh install        # binary bootstrap + agent picker (Part 1/2)
cmesh setup          # alias: run the agent picker again
cmesh up             # daemon (unchanged) + `up service …` (unchanged)
cmesh doctor         # diagnostics (unchanged, maybe folded reporting)
cmesh peers          # list/test (unchanged)
cmesh mcp --stdio    # the onboarding MCP (now wizard-driven)
cmesh self-install   # copy binary to PATH
```

Retire / fold the legacy compatibility aliases (`connect`, `test`, `status`,
`log`, `hello`, bare `init`):
- `init` → superseded by the MCP `configure_node`; keep a thin
  `cmesh init` that prints "run the agent picker / talk to the MCP" or hard-keep
  for headless/scripted setup behind `--yes`. (Recommend: keep headless `init`
  for CI, drop interactive `init` in favor of the wizard.)
- `connect` → folded into `register_gateway`.
- `test`/`status`/`log`/`hello` → move under `peers test`, `doctor`, etc., or drop.

Refactor service install/start out of `cmd/cmesh/main.go` into a callable
`internal/service` package so both the CLI and the `start_service` MCP tool share
one implementation (no logic duplicated across the seam).

## Part 6b — Uninstaller (mirror of the installer)

A hand-rolled `x/term` flow in the same style as the Part-2 installer, exposed as
`cmesh uninstall`. Core principle reminder: **the only real install is the cmesh
binary + the MCP registration.** So the uninstaller's job is narrow and clean:

1. **Detect** which agents currently have a cmesh MCP registered (stdio setup
   server and/or HTTP gateway). Show **only** those (hide agents without cmesh).
2. **Multi-select** which agents to remove cmesh from (same arrow/space TUI;
   defaults to all detected).
3. Echo what will happen, **confirm Y/N**.
4. Remove the MCP entries:
   - Claude Code: `claude mcp remove --scope user cmesh`.
   - Codex: strip the `[mcp_servers.cmesh]` table from `~/.codex/config.toml`
     (reuse `removeTable`).
5. **Optional, separately consented** cleanup (each its own Y/N, never bundled):
   - Stop + remove the OS service (launchd/systemd/Windows SC) via the shared
     `internal/service` package.
   - Remove the config file (`~/.config/cmesh/config.toml`).
   - Remove the data dir (`~/.local/share/cmesh`: db, audit, logs, artifacts,
     worktrees, task tokens) — call out that this deletes audit history.
   - Remove the binary itself / PATH alias (only if installed via
     `self-install`/bootstrap; print manual hint otherwise).
6. Honest final report of what was removed and what was left.

Design rules: detect-don't-ask; every destructive step independently confirmed;
safe to run twice (idempotent); never delete something cmesh didn't create.
`setupinstall` gains `DetectInstalledAgents()` (which agents have cmesh wired) and
`Uninstall(agent)` mirroring `Install(agent)`.

## Part 7 — Docs + examples

- Rewrite `docs/quickstart.md` around the new flow (installer → "help me setup
  cmesh" → two-node dance).
- Update `README.md` "Get started" (currently lists `init`/`doctor`/`up`/`peers`).
- Update `docs/architecture.md` request-lifecycle intro to mention the wizard.
- Keep `examples/config` in sync with the whois-name allowlist fix.

## Build order (incremental, each independently shippable)

1. **Part 0 fix + Hello identity** — write whois-matchable names in
   `GenerateConfig`; add `node_name`/`stable_id` to `HelloResponse`. (Small,
   high-value, unblocks the handshake. Land first.)
2. **`internal/service` extraction** — shared service install/start.
3. **Stdio wizard** — `onboarding_status`, `configure_node`, `start_service`,
   `link_peer`, `register_gateway`, `setup-cmesh` prompt. Verify-don't-ask.
4. **Native installer** — `x/term` multi-select + Y/N + stdio self-test +
   `self-install`.
5. **Uninstaller** — `cmesh uninstall`, mirror of the installer (Part 6b);
   `DetectInstalledAgents` + `Uninstall`.
6. **CLI restructure** — new command surface, retire aliases, update usage.
7. **Bootstrap script** — `scripts/install.sh` / `get.cmesh.dev`.
8. **Docs + examples.**

## Risks / call-outs

- **Codex gateway registration** varies by CLI version → always offer printed
  manual fallback; never hard-fail the wizard on it.
- **Service install needs an absolute runner path** (doctor already checks this);
  `configure_node` should resolve and write absolute `command[0]`.
- **macOS launchd `PATH`** for the service is already handled (`launchdPATH`);
  ensure the wizard's `start_service` reuses it so runner auth survives.
- **Two restarts collapsed to one**: sequence registration so the user restarts
  the agent session exactly once at the end.
- **Idempotency/resumability**: every tool must be safe to call twice; rely on
  `onboarding_status` re-derivation, not stored wizard state.
