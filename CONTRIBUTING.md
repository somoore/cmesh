# Contributing

cmesh can cause real changes on real machines. Contributions should preserve the default posture: trusted private networks, narrow policies, explicit high-risk profiles, and auditable results.

## Local checks

Run the full check set before opening a PR:

```bash
make test
```

For a faster loop while editing Go code:

```bash
make fmt
go test ./...
```

When editing workflows, run:

```bash
make workflow-lint
```

When editing schemas or example config, parse the changed JSON and TOML files before committing. See [docs/development.md](docs/development.md) for the validation snippet.

## Security-sensitive changes

Treat these as security-sensitive and add focused tests:

- peer identity matching;
- cwd normalization or allowlist behavior;
- profile selection, profile ceilings, or lease validation;
- task ownership, task tokens, and artifact access;
- runner command construction;
- audit logging;
- setup or repair flows that write config.

Prefer one clear denial path with an explicit error code over a best-effort fallback. Missing telemetry, missing identity, or missing policy evidence should be visible to the caller.

## Pull request expectations

- Keep changes scoped to the behavior under review.
- Add or update tests for policy, security, or user-facing behavior changes.
- Update docs when changing profiles, config fields, commands, or security boundaries.
- Do not commit release binaries, local config, credentials, or generated machine state.
- Keep GitHub Actions and third-party tool references pinned.

## License

By contributing, you agree that your contribution is licensed under the Apache License, Version 2.0.
