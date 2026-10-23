#!/usr/bin/env sh
set -eu

app=$1
version=$2
commit=$3
date=$4
dist=$5
# No -s -w: keep the symbol table so `govulncheck -mode binary` checks
# reachability per symbol instead of flagging every required module.
ldflags="-X cmesh/internal/version.Version=$version -X cmesh/internal/version.Commit=$commit -X cmesh/internal/version.Date=$date"

platforms=${CMESH_PLATFORMS:-"darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64"}

rm -rf "$dist"
mkdir -p "$dist"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

for platform in $platforms; do
  goos=${platform%/*}
  goarch=${platform#*/}
  name="${app}-${version}-${goos}-${goarch}"
  outdir="$tmp/$name"
  mkdir -p "$outdir"
  bin="$outdir/$app"
  if [ "$goos" = "windows" ]; then
    bin="$outdir/$app.exe"
  fi
  echo "building $name"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -ldflags "$ldflags" -o "$bin" ./cmd/cmesh
  if [ "$goos" = "darwin" ] && [ -n "${CMESH_MACOS_SIGN_IDENTITY:-}" ]; then
    echo "codesigning $name"
    codesign --force --timestamp --options runtime --sign "$CMESH_MACOS_SIGN_IDENTITY" "$bin"
    codesign --verify --strict --verbose=2 "$bin"
  fi
  cp README.md "$outdir/README.md"
  tar -C "$outdir" -czf "$dist/${app}_${goos}_${goarch}.tar.gz" .
done

if command -v sha256sum >/dev/null 2>&1; then
  (cd "$dist" && sha256sum *.tar.gz > checksums.txt)
else
  (cd "$dist" && shasum -a 256 *.tar.gz > checksums.txt)
fi
