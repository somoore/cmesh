# Runner Manifests

Runner manifests are evidence-backed capability declarations. They prevent cmesh from overclaiming what a harness profile enforces.

The schema is `schemas/runner_manifest.v1.json`.

## Manifest Rules

A runner manifest must declare:

- runner name and version;
- manifest version and generation time;
- profiles;
- capability claims per profile;
- exact native controls or flags;
- credential source;
- billing route;
- enforcement confidence;
- known gaps;
- bare or non-bare behavior;
- network and git push posture.

The daemon loads manifests at startup and stores the active manifest in SQLite for audit correlation. A task audit event records requested, granted, and used controls.

## Enforcement Confidence

Use `high` only when the runner profile has strong native controls and the manifest author can explain them precisely.

Use `medium` when the profile narrows behavior but still depends on harness semantics, shell wrappers, local config, or user auth state.

Use `low` when cmesh can describe intent but enforcement is mostly advisory.

Do not claim capabilities the runner does not enforce.

## Claude Code Manifest Example

```json
{
  "schema_version": "runner_manifest.v1",
  "runner": "claude-code",
  "runner_version": "1.0.0",
  "manifest_version": "2026-06-12",
  "generated_at": "2026-06-12T00:00:00Z",
  "platforms": ["darwin", "linux"],
  "default_profile": "dev",
  "profiles": {
    "readonly": {
      "claims": ["read_files"],
      "native_flags": ["--permission-mode", "plan", "--bare"],
      "native_controls": {
        "permission_mode": "plan",
        "bare": true
      },
      "credential_source": "ANTHROPIC_API_KEY or apiKeyHelper",
      "billing": "api",
      "enforcement_confidence": "medium",
      "known_gaps": [
        "Bare mode loses ambient project guidance such as CLAUDE.md.",
        "Credential and billing route depends on the configured API key or helper."
      ],
      "bare": true,
      "network": "unknown",
      "git_push": false,
      "max_prompt_bytes": 200000,
      "max_result_bytes": 200000
    },
    "dev": {
      "claims": ["read_files", "edit_repo", "execute_repo_commands"],
      "native_flags": ["-p"],
      "native_controls": {
        "headless_prompt": true,
        "skip_all_permissions": false
      },
      "credential_source": "subscription_login",
      "billing": "subscription",
      "enforcement_confidence": "medium",
      "known_gaps": [
        "Non-bare mode can inherit ambient hooks, skills, plugins, MCP servers, and repo guidance.",
        "Native tool permission behavior must be checked against the installed Claude Code version."
      ],
      "bare": false,
      "network": "unknown",
      "git_push": false,
      "max_prompt_bytes": 200000,
      "max_result_bytes": 200000
    }
  }
}
```

## Codex Manifest Example

```json
{
  "schema_version": "runner_manifest.v1",
  "runner": "codex",
  "runner_version": "1.0.0",
  "manifest_version": "2026-06-12",
  "generated_at": "2026-06-12T00:00:00Z",
  "platforms": ["darwin", "linux", "windows"],
  "default_profile": "dev",
  "profiles": {
    "dev": {
      "claims": ["read_files", "edit_repo", "execute_repo_commands"],
      "native_flags": [],
      "native_controls": {
        "sandbox": "workspace-write",
        "approval": "on-request",
        "network": "disabled_or_limited"
      },
      "credential_source": "local_config",
      "billing": "unknown",
      "enforcement_confidence": "medium",
      "known_gaps": [
        "Approval and sandbox are separate axes.",
        "Network behavior depends on CLI configuration and wrapper policy."
      ],
      "bare": false,
      "network": "limited",
      "git_push": false,
      "max_prompt_bytes": 200000,
      "max_result_bytes": 200000
    }
  }
}
```

## Bare Mode Requirements

Bare mode is profile-controlled and never implicit.

For a bare profile:

- validate the declared credential route before spawn;
- return `profile_unavailable` when credentials are missing;
- never fall back to non-bare;
- audit the credential route name, not secret values;
- declare billing as API, subscription, local, or unknown.

For Claude Code bare profiles:

- bare mode requires API credentials such as `ANTHROPIC_API_KEY` or an API key helper;
- bare mode does not inherit subscription login in the same way as non-bare operation;
- bare mode loses ambient project guidance and local harness extensions.

## Audit Integration

Every task should write audit fields for:

- requested profile;
- effective profile;
- requested claims;
- granted claims;
- used claims;
- runner controls;
- manifest version.

The audit record lets a reviewer answer:

```text
What did the origin ask for?
What did cmesh allow?
What controls did the runner claim to use?
What actually happened?
```
