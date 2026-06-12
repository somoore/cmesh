#!/usr/bin/env sh
set -eu

app=$1
version=$2
commit=$3
date=$4
dist=$5
ldflags="-s -w -X cmesh/internal/version.Version=$version -X cmesh/internal/version.Commit=$commit -X cmesh/internal/version.Date=$date"

platforms="darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64"

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
  cp README.md "$outdir/README.md"
	tar -C "$tmp" -czf "$dist/$name.tar.gz" "$name"
done

(cd "$dist" && sha256sum * > checksums.txt)
