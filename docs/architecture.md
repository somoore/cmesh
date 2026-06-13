# Architecture

cmesh is a local-first delegation plane for coding agents. It separates the model-facing tool surface from the daemon-to-daemon transport so remote machines can execute work without exposing MCP over the network.

There are two MCP surfaces:

- `cmesh mcp --stdio`: setup MCP launched directly by an AI coding agent before the daemon exists. It discovers Tailscale/runners/peers, suggests config, validates config, and writes config only after explicit confirmation.
- `http://127.0.0.1:7777/mcp`: local HTTP gateway served by `cmesh up` after setup. This is the delegation surface agents use for tasks.

## End-to-end diagram

```mermaid
flowchart LR
  subgraph Origin["Origin machine: mac-studio"]
    Agent["Agent harness<br/>Claude Code / Codex / other"]
    SetupMCP["setup MCP<br/>cmesh mcp --stdio<br/>pre-daemon onboarding"]
    MCP["cmesh local MCP gateway<br/>127.0.0.1:7777/mcp"]
    OP["Outbound policy<br/>peer/profile/hop checks"]
    OStore["SQLite task mirror<br/>peer_tasks, events"]
    OAudit["audit.jsonl"]
  end

  subgraph Tailnet["Tailscale tailnet"]
    RPC["Mesh RPC<br/>HTTP+JSON v1<br/>schema-versioned<br/>idempotent"]
    Whois["tailscaled LocalAPI<br/>whois(remote_addr)"]
  end

  subgraph Worker["Worker machine: homelab-vm"]
    Mesh["cmesh mesh RPC server<br/>100.x.y.z:7778"]
    IP["Tailnet source check"]
    Identity["Identity resolution<br/>node/user/tags"]
    WP["Inbound policy<br/>allowed peer<br/>realpath cwd<br/>profile ceiling<br/>runner availability"]
    WStore["SQLite durable store<br/>tasks, idempotency_keys,<br/>task_events, runner_manifests"]
    WAudit["audit.jsonl"]
    Git["Git contract<br/>clean worktree check<br/>cmesh/task branch<br/>local commit + footers"]
    Runner["Runner adapter<br/>Claude Code / Codex"]
    Proc["Worker process group<br/>captured stdout/stderr<br/>transcript refs"]
    Result["Canonical result envelope<br/>worker_summary_untrusted<br/>diffstat, files, tests,<br/>verification_required"]
  end

  Agent -. "first-run setup" .-> SetupMCP
  SetupMCP -. "writes config after confirmation" .-> MCP
  Agent -->|"tools/list, tools/call"| MCP
  MCP --> OP
  OP -->|"task.dispatch.requested"| OAudit
  OP --> OStore
  OP -->|"POST /v1/tasks"| RPC
  RPC --> Mesh
  Mesh --> IP
  IP --> Whois
  Whois --> Identity
  Identity --> WP
  WP -->|"denied"| WAudit
  WP -->|"accepted"| WStore
  WStore --> WAudit
  WP --> Git
  Git --> Runner
  Runner --> Proc
  Proc --> Result
  Result --> WStore
  Result --> WAudit
  Agent -->|"task_status / task_result"| MCP
  MCP -->|"GET /v1/tasks/{id}<br/>GET /v1/tasks/{id}/result"| RPC
  RPC --> WStore
  WStore --> Result
  Result --> MCP
  MCP --> Agent
```

## Request lifecycle

Before the daemon exists, `cmesh mcp` can install a setup MCP entry into Claude Code or Codex. That setup server runs over stdio and can guide onboarding without exposing any network listener.

Once `cmesh up` is running:

1. The agent calls the local MCP tool, usually `delegate`.
2. The loopback gateway validates local HTTP hardening rules:
   - loopback host only;
   - loopback origin only;
   - no forwarded identity headers.
3. The origin daemon applies outbound policy.
4. The origin daemon writes an audit event and mirrors the peer task locally.
5. The origin daemon sends `POST /v1/tasks` to the configured peer over Tailscale.
6. The worker daemon rejects non-tailnet sources before policy evaluation.
7. The worker daemon asks Tailscale LocalAPI `whois` for the inbound peer identity.
8. The worker daemon applies inbound policy:
   - allowed peer;
   - hop limit;
   - cycle detection;
   - realpath-normalized cwd allowlist;
   - configured profile;
   - configured runner.
9. The worker daemon creates or replays the task through the idempotency table.
10. The worker daemon prepares the git contract:
    - refuse dirty worktrees unless `allow_dirty=true`;
    - create deterministic `cmesh/<task-id>/<slug>` branch;
    - commit with cmesh metadata footers after runner execution.
11. The runner adapter launches the local worker process with captured logs.
12. The worker stores a bounded canonical result envelope.
13. The origin agent polls `task_status` and fetches `task_result`.

## Trust boundaries

```mermaid
sequenceDiagram
  autonumber
  participant A as Agent harness
  participant L as Local cmesh gateway
  participant O as Origin policy/store
  participant T as Tailscale
  participant W as Worker cmesh daemon
  participant R as Runner process

  A->>L: MCP tools/call delegate()
  L->>L: Validate loopback Host/Origin
  L->>O: Enforce outbound policy
  O->>T: Mesh RPC over tailnet
  T->>W: Authenticated tailnet connection
  W->>W: Reject non-tailnet source
  W->>T: LocalAPI whois(remote_addr)
  T-->>W: Node/user/tag identity
  W->>W: Enforce inbound policy
  W->>W: Create idempotent durable task
  W->>R: Spawn local runner process
  R-->>W: Output, logs, changed files
  W->>W: Store result envelope and audit
  A->>L: task_result(task_id)
  L->>W: GET /v1/tasks/{id}/result
  W-->>L: Bounded result envelope
  L-->>A: Untrusted worker claims
```

The important split:

- Tailscale decides who is connected.
- cmesh policy decides what that peer may do.
- The runner decides how a local agent process is invoked.
- Git remains the artifact transport.

## Data model

```mermaid
erDiagram
  tasks ||--o{ task_events : records
  tasks ||--o| idempotency_keys : keyed_by
  tasks ||--o| task_results : produces
  peer_tasks }o--|| tasks : mirrors
  runner_manifests ||--o{ tasks : informs

  tasks {
    text task_id PK
    text origin_node_id
    text origin_session_id
    text worker_node_id
    text state
    text cwd
    text runner
    text profile_requested
    text profile_effective
    text prompt_hash
    text payload_hash
    text result_ref
  }

  idempotency_keys {
    text key PK
    text task_id FK
    text origin_node_id
    text origin_session_id
    text payload_hash
    text retain_until
  }

  task_events {
    text event_id PK
    text task_id FK
    text event_type
    text node_id
    text timestamp
    text payload_json
  }

  peer_tasks {
    text task_id PK
    text peer_node_id
    text remote_task_id
    text state
    text result_ref
  }

  runner_manifests {
    text runner PK
    text version
    text manifest_json
  }

  task_results {
    text task_id PK
    text result_json
  }
```

## Failure semantics

cmesh is async-first and durable. Once `delegate` returns a task id, later sessions can query the task.

Worker daemon restart is fail-closed in v1:

- queued/running tasks are marked `failed`;
- error code is `worker_daemon_restarted`;
- audit records the transition;
- silent task loss is forbidden.

Origin restart reloads mirrored peer tasks from SQLite and continues polling the worker.
