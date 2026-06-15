#!/usr/bin/env sh
set -eu

tag=$1
dist=$2

if [ -z "$tag" ]; then
  echo "missing release tag"
  exit 1
fi

mkdir -p "$dist"
notes="$dist/release-notes.md"
target_commit=$(git rev-list -n 1 "$tag")
release_date=$(git show -s --format=%cI "$target_commit")
previous_tag=$(git describe --tags --abbrev=0 "${tag}^" 2>/dev/null || true)

if [ -n "$previous_tag" ]; then
  range="$previous_tag..$tag"
  range_label="$previous_tag..$tag"
else
  range="$tag"
  range_label="initial release through $tag"
fi

{
  printf '# cmesh %s\n\n' "$tag"
  printf 'Pre-alpha release built from signed tag `%s` at commit `%s`.\n\n' "$tag" "$target_commit"
  printf '> cmesh is experimental software. Use it on trusted private networks with narrow policies, and review the security documentation before exposing it to additional peers.\n\n'
  printf '## Release Date\n\n'
  printf '%s\n\n' "$release_date"
  printf '## Platform Assets\n\n'
  printf '%s\n' '- `cmesh_darwin_amd64.tar.gz` - macOS Intel'
  printf '%s\n' '- `cmesh_darwin_arm64.tar.gz` - macOS Apple silicon'
  printf '%s\n' '- `cmesh_linux_amd64.tar.gz` - Linux/Ubuntu x86_64'
  printf '%s\n' '- `cmesh_linux_arm64.tar.gz` - Linux/Ubuntu ARM64'
  printf '%s\n' '- `cmesh_windows_amd64.tar.gz` - Windows x86_64'
  printf '%s\n\n' '- `cmesh_windows_arm64.tar.gz` - Windows ARM64'
  printf '## Supply Chain\n\n'
  printf '%s\n' '- macOS binaries are signed with Developer ID Application and submitted for notarization before publication.'
  printf '%s\n' '- Release archives, SBOMs, checksums, and these release notes are signed with Sigstore cosign keyless signing.'
  printf '%s\n' '- Each release archive has a CycloneDX JSON SBOM named `<archive>.sbom.cdx.json`.'
  printf '%s\n\n' '- `checksums.txt` contains SHA-256 digests for release archives, SBOMs, and release notes.'
  printf '## Changelog\n\n'
  printf 'Changes included in `%s`:\n\n' "$range_label"
  git log --no-merges --pretty='- %s (%h)' "$range"
} > "$notes"
