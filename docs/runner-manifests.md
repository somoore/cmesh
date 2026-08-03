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

## See also

- [Configuration](configuration.md) — how profile claims, `bare`, and `enforcement_confidence` are set in the config file.
- [Security](security.md) — why manifests are treated as evidence, not enforcement, and where that boundary sits.

## Bring your own runner

cmesh ships adapters for Claude Code and Codex because each has argv quirks worth encoding carefully. Any other CLI coding agent can join the mesh from configuration alone, using the `generic` adapter.

What the built-in adapters actually enforce is small and portable, and the generic adapter takes those four as config: the prompt goes on **stdin, never argv**; profile authority maps to whatever flags that CLI uses; output is parsed when the CLI promises JSON and passed through raw otherwise; and the manifest declares capabilities honestly.

```toml
[runners.acme-agent]
type = "generic"
command = ["/usr/local/bin/acme"]
default_profile = "repo-dev"
# Required for a generic runner: it must declare what it enforces.
manifest_path = "~/.config/cmesh/manifests/acme-agent.runner_manifest.v1.json"

[profiles.repo-dev]
runner = "acme-agent"
capabilities = ["read_files", "edit_repo", "execute_in_repo"]
writes = "repo_only"
shell = "allow"
require_explicit = true
require_lease = true
# Whatever flags this CLI uses to express this profile's authority.
runner_args = ["--sandbox", "workspace"]
# Appended last, for a CLI that needs a trailing marker to read stdin.
runner_prompt_args = ["-"]
# Set only if the CLI promises JSON on stdout.
runner_structured_output = false
```

Three constraints are deliberate:

- **`manifest_path` is required.** A generic runner cmesh has never seen must say what it enforces, including its `known_gaps`. A runner that will not declare itself does not run.
- **`enforcement_confidence: high` is impossible.** That level is reserved for the built-in probe runner, whose behavior cmesh implements. A third-party CLI cannot claim it, in config or in a manifest file.
- **The prompt is always on stdin.** A generic runner cannot opt into argv delivery, because cmesh cannot verify a third-party CLI's argument handling.

Verify the result rather than trusting the declaration:

```bash
cmesh doctor runner acme-agent
```

That runs the same behavioral smoke used for the built-in adapters — it resolves the command, checks each profile, feeds a prompt over stdin, and confirms the response came back through the raw `command_output` artifact. A runner that passes has demonstrated the contract, not asserted it.
