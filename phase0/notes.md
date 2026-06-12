# Phase 0 Manual Delegation Notes

Phase 0 validates cmesh behavior before daemon code. Use real work, real machines, and real constraints. Record every delegation in `phase0/delegations.jsonl` as one JSON object per line.

## Bare-Mode Auth Results

Run on each worker candidate:

```bash
claude -p --bare --output-format json "summarize this repo"
```

Record results in this table as machines are tested:

| Machine | OS | Claude Code version | Bare worked | Credential source | Billing route | Non-bare worked | Failure text |
| --- | --- | --- | --- | --- | --- | --- | --- |

Allowed credential source values:

- `ANTHROPIC_API_KEY`;
- `apiKeyHelper`;
- `subscription_login`;
- `local_config`;
- `neither`.

Allowed billing route values:

- `api`;
- `subscription`;
- `local`;
- `unknown`.

## Delegation Record Fields

Each JSONL entry uses these fields:

```json
{
  "id": "phase0-001",
  "timestamp": "2026-06-12T00:00:00Z",
  "origin_machine": "mac-studio",
  "worker_machine": "homelab-vm",
  "reason_for_delegation": "linux build",
  "cwd": "~/code/cmesh",
  "desired_profile": "dev",
  "prompt_file": "phase0/prompts/phase0-001.md",
  "expected_result_shape": ["summary", "branch", "test_log", "diffstat"],
  "actual_result_shape": ["summary", "test_log"],
  "files_changed": [],
  "human_intervention_needed": false,
  "time_to_result_s": 0,
  "would_delegate_again": true,
  "awkwardness_notes": ""
}
```

## Required Delegation Mix

Track at least ten real delegations across these categories:

1. Linux build or test from macOS origin.
2. Long-running compile.
3. Repo summarization or read-only task.
4. Risky operation on snapshot VM.
5. Toolchain-specific task.
6. Task that fails due to missing permission.
7. Task that produces code changes.
8. Task that produces no code changes but useful verdict.
9. Task where transcript or logs are useful.
10. Task where the result envelope is awkward or insufficient.

## Heuristic Observations

Use this section to rank which rules mattered in practice:

| Heuristic | Count | Keep for v1 guidance |
| --- | ---: | --- |

## Phase 0 Decisions

Record decisions when evidence supports a call:

| Decision | Outcome | Evidence |
| --- | --- | --- |
