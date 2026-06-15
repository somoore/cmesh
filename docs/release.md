# Release Process

> Experimental project: releases should not be presented as enterprise-hardened remote management software. Keep release notes explicit about trusted private networks, narrow policies, and known security gaps.

cmesh releases are source-built by GitHub Actions from signed version tags.

## Local checks

Run the same checks CI runs:

```bash
make test
```

This includes:

- `gofmt` check;
- `go mod tidy` check;
- Go package loading;
- nested module detection;
- floating module, tool, and container reference detection;
- GitHub Action SHA pinning checks;
- duplicate Go code detection with `dupl`;
- vulnerability scanning with `govulncheck`;
- unit tests.

## Build artifacts

Build all release archives locally:

```bash
make build VERSION=v0.1.0
```

Artifacts are written to `dist/`:

- `cmesh-vX.Y.Z-darwin-amd64.tar.gz`
- `cmesh-vX.Y.Z-darwin-arm64.tar.gz`
- `cmesh-vX.Y.Z-linux-amd64.tar.gz`
- `cmesh-vX.Y.Z-linux-arm64.tar.gz`
- `cmesh-vX.Y.Z-windows-amd64.tar.gz`
- `checksums.txt`

## Signing

Release workflows sign archives and `checksums.txt` with Sigstore cosign keyless signing.

Each signed artifact gets:

- `.sig` signature file;
- `.pem` signing certificate file.

## Cut a release

Use a signed git tag:

```bash
make deploy VERSION=v0.1.0
```

`make deploy` runs `make test`, requires a clean working tree, creates a signed tag, and pushes that tag. The GitHub release workflow then builds, signs, and uploads release assets.

## Supply-chain rules

- Do not commit release binaries to the repository.
- Do not use floating module or tool references.
- Do not use floating container image tags.
- Pin GitHub Actions to full commit SHAs.
- Prefer signed tags for releases.
- Keep `go.sum` reviewed and committed.

## See also

- [Development](development.md) — the build, test, and pre-commit workflow that precedes a release.
- [Security](security.md) — the security posture release notes should keep explicit.
