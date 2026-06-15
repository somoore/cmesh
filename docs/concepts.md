# Concepts

> Experimental: cmesh is an early agent delegation mesh for trusted private networks. It is not enterprise remote management software, and every worker profile is real authority on that machine. Do not use it on anything you love.

This is the mental-model doc. If you want the wiring, read [architecture.md](architecture.md); if you want the threat model, read [security.md](security.md). This page is about the idea and why it's worth caring about.

## The core idea: agent-to-agent delegation

A coding agent is powerful but stuck. Claude Code or Codex runs on one machine, sees one filesystem, talks to one set of toolchains and one set of credentials. cmesh lets that agent hand a scoped task to a coding agent on **another** machine you control, and get a structured result back — without you leaving the session you're already in.

It is not remote shell, not screen sharing, not a fleet-management console. It is one agent asking another agent, on a machine you own, to do a specific piece of bounded work and report back. The asking happens as a normal tool call. The work happens locally on the far machine, through that machine's own runner and its own credentials. The result comes home as a durable, auditable envelope.

## Why it exists

Coding agents are usually trapped on one machine, but real development work is not.

You might keep a Linux box for production-like builds, a Mac for Apple targets, a Windows machine for native tests, a snapshot VM for risky edits, or a bare-metal server holding the exact service state you need to inspect. Today, using those machines from your agent means dropping out of the session: SSH in by hand, re-paste the prompt, lose the surrounding context, eyeball permissions, and end up with no durable record of what ran or what it produced.

cmesh makes that handoff a tool call instead of a manual ritual. The agent stays in flow, the work runs where it belongs, and every dispatch, denial, and result lands in an audit log you can read later.

## Mental models

These five framings explain most of how cmesh behaves. Internalize them and the rest of the system reads as obvious.

### 1. Task-driven, not telemetry-driven

cmesh does not poll your machines, scrape metrics, or maintain a live dashboard of everything everywhere. Nothing happens until a human or an agent asks a **specific** peer to do **specific** work. The peer evaluates that request against policy, runs it locally, and returns a result. There is no ambient surveillance layer — only discrete, named, auditable tasks. This keeps the trust surface small: a machine that is never asked to do anything does nothing.

### 2. Profiles are authority levels

Every task runs under a **profile**, and a profile is a declared level of authority on the worker machine — from "look, don't touch" up to "change the system." The menu runs roughly least-to-most authority: `query` (deterministic probes), `query-network` and `query-shell` (bounded read-oriented checks), `readonly` / `readonly-agent` (agent-assisted read-only inspection), `repo-dev` / `dev` (agent edits and tests inside an allowed directory), and the elevated tier — `desktop-control`, `system-change`, `network`, `interactive`, `git_push`. You pick a profile per task by naming it in the request. A peer only offers the profiles you configured for it, so granting `repo-dev` to one trusted machine never grants it to the rest.

### 3. `query` is the safe default

The default generated profile is `query`, and it is deliberately the least dangerous thing cmesh can do. Under `query`, cmesh runs its own **built-in deterministic probes** — not Claude Code, not Codex — and the profile denies shell and denies writes. So the out-of-the-box behavior is "ask the machine a structured question and get a structured answer," with no arbitrary agent execution involved. You reach for agent-backed and elevated profiles only when a typed probe genuinely isn't enough, and you grant them only to peers you trust, scoped to those peers.

### 4. Tailscale decides who's connected; cmesh decides what they may do

This is the load-bearing split. Tailscale provides encrypted transport and an authenticated peer identity — it answers *who connected*. It does **not** answer *what they're allowed to do*. Being reachable on the tailnet is never permission to run code. After Tailscale establishes who the caller is (via LocalAPI `whois` on the real connection, not on any self-reported name or header), cmesh applies its own policy: is this peer in the allowlist, is this directory permitted, is this profile allowed, is the runner available, is the hop limit respected. Network reachability is only the first gate.

### 5. Worker output is untrusted claims

When a task comes back, the worker's prose summary is presentation-only — it is labeled as untrusted and is never treated as ground truth. The trustworthy parts are the structured, captured facts: the redacted command, raw stdout and stderr, exit code, diffstat, changed files, test results. For code changes, the real artifact is a git branch you can review, not a paragraph claiming success. The mental shift: a remote agent's narration is a hint, the captured envelope is the evidence, and high-risk results are verified locally before you act on them.

## What you could do with it

Concrete things that become a single sentence to your agent once a mesh is up:

- "Ask the Linux box to run the full test suite in `~/code/myapp` and bring back the results" — production-like CI without leaving your Mac.
- "Ask the datacenter server why the deploy failed" — inspect service state and logs on a machine that has the real failure, not a reproduction.
- "Ask the Windows machine to run the native build and tests" — exercise a platform you don't have in front of you.
- "Ask the homelab to report disk usage" — a structured `query` answer with no shell and no writes (add `query-network` when you also need bounded port/connectivity checks).
- "Ask the snapshot VM to attempt the risky migration on a branch" — let an agent try something dangerous on a machine you can roll back.
- "Ask the build server to summarize this repo and its recent git history" — borrow another machine's checkout and toolchain for read-only context.

In each case your agent stays where it is, the work happens on the right machine through that machine's own runner, and you get back something you can audit.

## Where to go next

- [architecture.md](architecture.md) — the two MCP surfaces, the full request lifecycle, trust boundaries, and the data model.
- [security.md](security.md) — how cmesh rejects illegitimate nodes, what it protects, what it does not, and how to harden a deployment.
- [quickstart.md](quickstart.md) — get a two-machine mesh running.
