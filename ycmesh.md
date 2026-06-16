# Why cmesh?

> Short answer: SSH plus tmux is the baseline. cmesh exists for the cases where remote access is not enough, and what you really want is policy-bound, auditable agent delegation.

cmesh is not trying to prove that SSH is obsolete. SSH is one of the best tools in computing. If the job is "log into another machine, run commands, and keep a terminal alive", SSH plus tmux is simpler, more mature, and easier to reason about than cmesh.

The bet behind cmesh is narrower: coding agents are becoming operators of real development workflows, but their default operating model is still single-machine. They can inspect one checkout, run one local toolchain, and use one local set of credentials. Real work does not fit that shape. A useful developer environment often spans a Mac, a Linux box, a Windows machine, a build server, a snapshot VM, and a machine that has the real broken service state.

SSH lets a human reach those machines. cmesh lets an agent delegate bounded work to them.

## The SSH plus tmux model

The practical alternative to cmesh is straightforward:

1. Open a terminal.
2. SSH to the target machine.
3. Attach or create a tmux session.
4. Start Claude Code, Codex, or another agent there.
5. Paste the task and enough context.
6. Watch the remote agent run.
7. Copy results back into the original session.

That works, and for many jobs it is the right answer.

It is also fundamentally an interactive remote terminal workflow. Its unit of control is a login session. Its authorization boundary is usually the OS account you logged in as. Its result is whatever is visible in a terminal buffer, plus whatever discipline you add around logs, branches, transcripts, and review.

cmesh starts from a different unit of work: a task.

## What changes when the unit is a task?

A cmesh task has a caller, a target peer, a working directory, a profile, a prompt, a task id, a timeout, a durable state, and a result envelope. The worker decides whether the authenticated caller may run that task before any runner starts. The origin can later fetch status, result, transcript, and audit evidence by id.

That structure is the difference.

With SSH, the question is usually:

```text
Can this account log into that machine?
```

With cmesh, the question is:

```text
Can this authenticated peer run this kind of task, in this directory, with this runner profile, right now?
```

That is the project thesis: cross-machine agent work should be an explicit, policy-checked, replayable task, not an informal remote shell ritual.

## Why not just script SSH better?

You can build pieces of cmesh on top of SSH. You can wrap commands, log output, standardize tmux names, and teach agents a playbook. That may be enough.

The hard part is not transport. The hard part is consistently enforcing the boundary around agent work:

- Which peer is allowed to ask?
- Which directory may it touch?
- Which authority level is being requested?
- Is this a read-only probe, a repo edit, a system change, or desktop control?
- Did the worker actually accept the task, or did a model merely claim it did?
- Where is the raw output?
- Can another peer read or cancel this task?
- Can the same request be retried without accidentally doing different work?
- After a daemon restart, can the origin still find the task?

SSH answers "who can log in". cmesh is trying to answer "what work may be delegated".

## The core differences

| Dimension | SSH plus tmux | cmesh |
| --- | --- | --- |
| Primary abstraction | Remote session | Durable task |
| Caller | Human at a terminal | Local agent tool call |
| Transport | SSH | Tailscale mesh RPC |
| Network exposure | SSH daemon | Mesh RPC on tailnet IP; MCP stays loopback-only |
| Authorization | OS account, SSH keys, sudoers, shell discipline | Tailscale identity plus cmesh peer, cwd, profile, lease, and policy checks |
| Authority shape | Whatever the login account can do | Named profiles such as `query`, `readonly-agent`, `repo-dev`, `system-change` |
| Result | Terminal output and files you inspect manually | Structured result envelope, state, transcript refs, audit events |
| Retry behavior | Manual | Idempotency-aware task creation |
| Multi-peer flow | Human orchestration | Peer-keyed delegation and result collection |
| Agent context | Re-paste or rehydrate manually | Local agent keeps the originating conversation |
| Failure mode | Terminal/session/tool-specific text | Stable task states and error codes |

This is not a claim that cmesh is always better. It is a claim that it is solving a different problem.

## What cmesh is aiming for

cmesh is aiming to become a small control plane for trusted personal and small-team agent meshes.

The target shape:

- You keep using the agent session you are already in.
- The agent can ask another machine to do work because that machine is better suited for it.
- The worker has its own local runner, credentials, filesystem, OS, and toolchain.
- The worker evaluates policy locally before doing anything.
- The task produces an auditable, bounded result.
- High-risk authority is explicit, leased where needed, and visible in the task record.
- The MCP surface is local-only; remote machines never expose your agent tool surface directly.
- The remote worker summary is treated as an untrusted claim, backed by captured output and artifacts.

The point is not "run commands remotely". The point is "make remote agent work reviewable, bounded, and recoverable".

## Where cmesh should be better than SSH

### Platform-specific work

You are on a Mac, but the failure only reproduces on Windows. With SSH plus tmux, you switch contexts and drive a second session. With cmesh, your current agent can delegate the Windows-native test and bring back the structured result.

### Production-like or hardware-specific environments

Some machines have the real kernel, GPU, service state, local database, or filesystem layout. cmesh lets the worker inspect that environment under a profile you configured instead of handing the origin a broad shell.

### Repeated cross-machine checks

Fan-out checks across machines are awkward by hand. cmesh can represent each peer's answer as its own task with its own state and result rather than merging several terminal sessions into one narrative.

### Risky work on disposable machines

For migrations, refactors, and destructive experiments, a snapshot VM is often the right place to try the work. cmesh makes "ask the snapshot worker to attempt it on a branch" a first-class flow.

### Audit and review

If a remote agent changes code, the useful artifact is not "the agent says it worked". The useful artifact is the branch, diffstat, tests, captured output, and task record. cmesh is built around that evidence boundary.

## Where SSH is still better

Use SSH plus tmux when:

- you need a real interactive terminal;
- you are debugging the machine itself and want direct control;
- the task is one-off and you do not need policy, audit, or structured results;
- you trust the remote account completely and broad shell access is appropriate;
- cmesh setup would be more work than the task;
- the runner needs rich interactive behavior that cmesh does not support yet.

cmesh should not hide that tradeoff. Extra structure costs something. If the structure does not buy you anything, SSH is the better tool.

## Non-goals

cmesh is not trying to be:

- a replacement for SSH;
- a general fleet management system;
- a remote desktop product;
- a production security boundary by itself;
- a sandbox for untrusted machines;
- a way to expose MCP over the network;
- a magic proof that a remote agent's prose is true.

It is a delegation layer for machines you already control and trust enough to include in a private mesh.

## Design principles

### Tailscale answers identity; cmesh answers authorization

Tailscale gives cmesh an authenticated peer identity over a private network. cmesh then decides what that peer may do. Network reachability alone is never permission to run a task.

### Local MCP only

The model-facing MCP server is local loopback. Remote machines receive mesh RPC tasks, not direct MCP calls. This keeps the agent tool surface off the network.

### Profiles are the vocabulary of authority

Profiles make authority explicit. `query` is not `repo-dev`; `repo-dev` is not `system-change`. If a peer needs more authority, the request should say so and policy should agree.

### Results are evidence, not vibes

Worker summaries are convenient, but they are not the source of truth. The captured command output, transcript refs, git branch, test output, and audit events are the evidence.

### Fail closed, leave a record

Denied tasks, failed tasks, timeouts, runner failures, and daemon restarts should become queryable state. Silent loss is worse than noisy failure.

## The thesis

SSH is how humans operate remote machines.

cmesh is an experiment in how agents should delegate work across machines: not by pretending every remote machine is just another terminal, but by making each handoff a scoped task with identity, policy, durable state, and auditable evidence.

That is the reason for the project.
