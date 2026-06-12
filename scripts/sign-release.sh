#!/usr/bin/env sh
set -eu

dist=$1
cosign_version=$2

if [ ! -d "$dist" ]; then
  echo "missing dist directory: $dist"
  exit 1
fi

for artifact in "$dist"/*.tar.gz "$dist"/checksums.txt; do
  [ -e "$artifact" ] || continue
  echo "signing $artifact"
  go run github.com/sigstore/cosign/v2/cmd/cosign@"$cosign_version" sign-blob \
    --yes \
    --output-signature "$artifact.sig" \
    --output-certificate "$artifact.pem" \
    "$artifact"
done
