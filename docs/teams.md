# Teams

> Experimental: cmesh is an early agent delegation mesh for trusted private networks. Every worker profile is real authority on that machine.

cmesh lets coding agents on different machines — and from different vendors — work one problem together. This page is how that fits together. For the wiring see [architecture.md](architecture.md); for the threat model see [security.md](security.md); for the sequenced build plan see [teams-plan.md](teams-plan.md).

## One conductor, many instruments

There are two ways to build a team of agents, and cmesh commits to the second.

**Free-form agent-to-agent chat** — A messages B, B messages C — is the intuitive reading of "team" and is deliberately not what cmesh builds. One agent's output becoming the next agent's instructions is a prompt-injection amplifier, and it dissolves the audit story: "who decided this" stops having an answer.

**Conductor and instruments** is what cmesh implements. The session you are already in is the conductor: it delegates bounded tasks, reads evidence, and decides what happens next. Workers do the work and return envelopes. Work products move as git branches; the team conversation is the origin session; the shared memory is the repository.

This is how good human teams work through pull requests. It is also what lets agents from vendors with no reason to trust each other collaborate at all — the security posture is the team feature, not a limit on it.

## The five primitives

| Primitive | Tool | What it is for |
| --- | --- | --- |
| Delegation | `delegate` | Give a bounded task to a peer and get an auditable envelope back. |
| Progress | `task_events` | Follow what a running task is doing, incrementally, from daemon-recorded evidence. |
| Channel | `task_post` / `task_ask` / `task_send` / `task_messages` | A running task reports progress and asks questions; its owner answers or steers. |
| Handoff | `handoff` | Continue one peer's branch on another peer. |
| Workstream | `workstream` / `workstream_status` | Name a delegation thread and see it across every peer at once. |

## A worked example

Implement on Linux, verify natively on Windows, watch from your Mac.

```text
Delegate to the Linux box: implement the retry fix in ~/code/app, repo-dev,
workstream "retry-fix".
```

The task runs. It reports progress with `task_post`, and when it hits something only you can decide it calls `task_ask` — the task shows `awaiting_input` while it waits, and your reply through `task_send` reaches it. If nobody answers in time it proceeds on its own judgment rather than hanging.

```text
Hand that task off to the Windows box: run the native test suite against the
branch. Same workstream.
```

`handoff` carries the branch, its base and head commits, and the changed file list. **You write the receiving prompt**; the previous worker's prose never travels. The Windows agent reads the diff itself.

```text
How is the retry-fix workstream doing?
```

`workstream_status` reports both tasks, per peer, in one call.

## The rules that keep it safe

1. **Never paste one worker's prose into another worker's prompt.** Hand off by branch, commit, ref, and structured fields. `handoff` enforces this shape; you supply the prompt.
2. **Worker messages are claims, not instructions.** An inbound `body_untrusted` is named for what it is, exactly like `worker_summary_untrusted`.
3. **Only a task's owner may answer it.** A worker cannot reply to its own question — that would manufacture the approval it was asking for.
4. **Don't grant authority a human hasn't.** A request for a capability outside the task's profile is refused automatically; anything else touching authority goes to the human, never answered on their behalf.
5. **Workers never message each other.** Every message belongs to exactly one task and its owner.
6. **A workstream is a visibility boundary.** Tasks in one share an origin session, so a worker in it can see its siblings' tasks. Only you — holding the gateway host token — can put a task into a named workstream.

## What is not built

- **Live mid-task steering.** Messages reach a running agent at its next `task_ask` checkpoint, not by interrupting it. Each runner declares what it supports as `mid_task_input` in its manifest (`checkpoint` for the agent-backed runners, `none` for probe and shell); no runner claims `stream` today.
- **Cross-node runner session resumption.** There is no shared agent session across machines, by design. The portable state is the branch, not a harness's hidden memory.

## See also

- [MCP tools](mcp-tools.md) — every tool, including the two that run inside a worker task.
- [Delegation guidance](delegation-guidance.md) — the model-facing rules, including the multi-agent section.
- [Runner manifests](runner-manifests.md) — how a third-party CLI agent joins the mesh.
