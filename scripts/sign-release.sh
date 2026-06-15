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
  attempt=1
  while :; do
    rm -f "$artifact.sig" "$artifact.pem"
    if go run github.com/sigstore/cosign/v2/cmd/cosign@"$cosign_version" sign-blob \
      --yes \
      --output-signature "$artifact.sig" \
      --output-certificate "$artifact.pem" \
      "$artifact"; then
      break
    fi
    if [ "$attempt" -ge 3 ]; then
      echo "failed to sign $artifact after $attempt attempts"
      exit 1
    fi
    attempt=$((attempt + 1))
    echo "retrying $artifact signing attempt $attempt"
    sleep 5
  done
done
