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

- `cmesh_darwin_amd64.tar.gz`
- `cmesh_darwin_arm64.tar.gz`
- `cmesh_linux_amd64.tar.gz`
- `cmesh_linux_arm64.tar.gz`
- `cmesh_windows_amd64.tar.gz`
- `cmesh_windows_arm64.tar.gz`
- `checksums.txt`

The release tag supplies the version. Archive names intentionally match the
installer scripts so `CMESH_REPO=owner/repo scripts/install.sh` and
`scripts/install.ps1` can download the right asset for the host platform.

## Signing

Release workflows sign archives and `checksums.txt` with Sigstore cosign keyless signing.

Each signed artifact gets:

- `.sig` signature file;
- `.pem` signing certificate file.

macOS release binaries are also signed with a Developer ID Application
certificate and submitted to Apple's notarization service before publication.

Required GitHub environment/secrets:

- `release-signing` environment;
- `RELEASE_SSH_ALLOWED_SIGNERS` repository secret containing the SSH
  allowed-signers entry that verifies release tags;
- `MACOS_DEVELOPER_ID_APPLICATION_IDENTITY`;
- `MACOS_DEVELOPER_ID_APPLICATION_P12_BASE64`;
- `MACOS_DEVELOPER_ID_APPLICATION_P12_PASSWORD`;
- `APPLE_API_KEY_ID`;
- `APPLE_API_ISSUER_ID`;
- `APPLE_API_KEY_P8_BASE64`.

Create `MACOS_DEVELOPER_ID_APPLICATION_P12_BASE64` from the exported Developer
ID Application `.p12` with:

```bash
base64 -i developer-id-application.p12 | pbcopy
```

Create `RELEASE_SSH_ALLOWED_SIGNERS` from the SSH signing key with:

```bash
printf '* %s\n' "$(cat ~/.ssh/id_ed25519_signing.pub)"
```

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
