#!/usr/bin/env sh
set -eu

dist=$1
cosign_version=$2

if [ ! -d "$dist" ]; then
  echo "missing dist directory: $dist"
  exit 1
fi

find "$dist" -maxdepth 1 -type f \( \
  -name '*.tar.gz' -o \
  -name '*.sbom.cdx.json' -o \
  -name 'checksums.txt' -o \
  -name 'release-notes.md' \
\) | sort | while IFS= read -r artifact; do
  echo "signing $artifact"
  go run github.com/sigstore/cosign/v2/cmd/cosign@"$cosign_version" sign-blob \
    --yes \
    --output-signature "$artifact.sig" \
    --output-certificate "$artifact.pem" \
    "$artifact"
done
