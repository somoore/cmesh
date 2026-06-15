# Runner Manifests

> Experimental: runner manifests are evidence, not magic. They describe what a local runner appears to enforce; they do not make cmesh an OS sandbox or enterprise remote-management system.

Runner manifests are evidence-backed capability declarations. They prevent cmesh from overclaiming what a harness profile enforces.

The manifest types are defined in `internal/runner/types.go`; each runner adapter
builds its own manifest from its configured profiles.

## Manifest Shape

A runner manifest is produced by the runner adapter (see `internal/runner/types.go`,
`CapabilityManifest`/`ProfileManifest`). At the top level it declares:

- `schema_version` — always `runner_manifest.v1`;
- `runner` — the runner name (`claude-code`, `codex`);
- `version` — optional runner version string;
- `profiles` — a map of profile name to profile manifest.

Each profile manifest declares:

- `claims` — capability claims for the profile (`read_files`, `edit_repo`, `execute_repo_commands`);
- `native_flags` — the exact CLI flags the adapter passes to the runner;
- `native_controls` — optional named native controls (string-to-string);
- `credential_source` — how the runner authenticates;
- `billing` — billing route (`api`, `subscription`, `local`, `unknown`);
- `enforcement_confidence` — `high`, `medium`, or `low`;
- `known_gaps` — caveats a reviewer should know;
- `bare` — whether the profile runs the runner in bare mode.

The daemon records the resolved profile manifest in SQLite for audit correlation. A task audit event records the requested profile, the effective profile, and the requested, granted, and used claims.

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
  "profiles": {
    "readonly": {
      "claims": ["read_files"],
      "native_flags": ["--permission-mode", "plan", "--bare"],
      "credential_source": "ANTHROPIC_API_KEY|apiKeyHelper",
      "billing": "api",
      "enforcement_confidence": "medium",
      "known_gaps": [
        "Bare mode loses ambient project guidance such as CLAUDE.md.",
        "Credential and billing route depends on the configured API key or helper."
      ],
      "bare": true
    },
    "dev": {
      "claims": ["read_files", "edit_repo", "execute_repo_commands"],
      "native_flags": ["-p"],
      "credential_source": "subscription_login",
      "billing": "subscription",
      "enforcement_confidence": "medium",
      "known_gaps": [
        "Non-bare mode can inherit ambient hooks, skills, plugins, MCP servers, and repo guidance.",
        "Native tool permission behavior must be checked against the installed Claude Code version."
      ],
      "bare": false
    }
  }
}
```

## Codex Manifest Example

```json
{
  "schema_version": "runner_manifest.v1",
  "runner": "codex",
  "profiles": {
    "dev": {
      "claims": ["read_files", "edit_repo", "execute_repo_commands"],
      "native_flags": ["--ask-for-approval", "on-request", "--sandbox", "workspace-write"],
      "native_controls": {
        "sandbox": "workspace-write",
        "approval": "on-request",
        "network": "disabled_or_limited"
      },
      "credential_source": "codex login or configured API credentials",
      "billing": "codex account",
      "enforcement_confidence": "medium",
      "known_gaps": [
        "Approval and sandbox are separate axes.",
        "Network behavior depends on CLI configuration and wrapper policy."
      ]
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

Every task writes audit fields for:

- requested profile;
- effective profile;
- requested claims;
- granted claims;
- used claims.

The audit record lets a reviewer answer:

```text
What did the origin ask for?
What did cmesh allow?
What controls did the runner claim to use?
What actually happened?
```
