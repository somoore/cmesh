# Development

> Experimental project: local development and testing can exercise real remote task execution. Use disposable or trusted machines, narrow policies, and assume worker profiles grant real authority.

## Build

```bash
go build -o cmesh ./cmd/cmesh
```

On Windows:

```powershell
go build -o cmesh.exe ./cmd/cmesh
```

## Test

```bash
make test
```

`make test` runs formatting checks, module hygiene, supply-chain checks, duplicate-code detection, vulnerability scanning, and unit tests.

For the current hardening paths, the most useful focused checks are:

```bash
go test ./internal/runner/...
go test ./internal/mesh/rpc
go test ./cmd/cmesh
```

Those cover stdin runner delivery, semantic smoke helpers, mesh task ownership authorization, forged provenance rejection, and the doctor service-context parser helpers.

## Format

```bash
make fmt
```

## Build release artifacts

```bash
make build VERSION=v0.1.0
```

Artifacts are written to `dist/`.

## Cut a release

```bash
make deploy VERSION=v0.1.0
```

This runs the full test suite, requires a clean tree, creates a signed git tag, and pushes the tag. GitHub Actions builds, signs, and publishes the release assets.

## Pre-commit hook

Install the repository hook:

```bash
make install-hooks
```

The hook runs `make precommit`, which includes the local checks that should stay fast enough for commit-time feedback.

## Validate docs and schemas

When editing schemas or example configs, parse the JSON and TOML before committing.

Using Python 3.11 or newer:

```bash
python - <<'PY'
import json, pathlib, tomllib
root = pathlib.Path(".")
for path in sorted(root.glob("schemas/*.json")) + sorted(root.glob("examples/**/*.json")):
    json.loads(path.read_text())
for path in sorted(root.glob("examples/config/*.toml")):
    tomllib.loads(path.read_text())
print("ok")
PY
```

## Package map

```text
cmd/cmesh                  CLI and daemon entrypoint
internal/audit             JSONL audit events
internal/config            TOML config loading and validation
internal/doctor            environment checks
internal/gitx              git branch, commit, diffstat contract
internal/identity          Tailscale LocalAPI identity
internal/mcp/gateway       loopback MCP gateway
internal/mcp/setupinstall  AI agent MCP installer/upsert helpers
internal/mcp/setupstdio    pre-daemon stdio setup MCP server
internal/mesh/rpc          daemon-to-daemon RPC server/client
internal/onboard           setup discovery and config generation
internal/policy            inbound/outbound policy engine
internal/profile           profile templates, capabilities, and guidance
internal/runner            runner abstraction
internal/runner/claudecode Claude Code runner
internal/runner/codex      Codex runner
internal/runner/shell      allowlisted-shell runner (query-shell)
internal/store             SQLite task store
internal/task              task state and result types
schemas                    JSON Schemas
examples                   runnable config and Tailscale grant examples
docs                       architecture and operator guidance
```

## See also

- [Release process](release.md) — how the checks above feed into tagged, signed releases.
- [Architecture](architecture.md) — the system the package map implements.
