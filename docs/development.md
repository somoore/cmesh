# Development

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
go test ./...
```

## Format

```bash
go fmt ./...
```

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
internal/mesh/rpc          daemon-to-daemon RPC server/client
internal/policy            inbound/outbound policy engine
internal/runner            runner abstraction
internal/runner/claudecode Claude Code runner
internal/runner/codex      Codex runner
internal/store             SQLite task store
internal/task              task state and result types
schemas                    JSON Schemas
examples                   runnable config and Tailscale grant examples
docs                       architecture and operator guidance
```

