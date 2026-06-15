#!/usr/bin/env sh
set -eu

dist=$1
syft_version=$2

if [ ! -d "$dist" ]; then
  echo "missing dist directory: $dist"
  exit 1
fi

found=0
for artifact in "$dist"/*.tar.gz; do
  [ -e "$artifact" ] || continue
  found=1
  sbom="$artifact.sbom.cdx.json"
  echo "generating SBOM for $artifact"
  go run github.com/anchore/syft/cmd/syft@"$syft_version" \
    "file:$artifact" \
    -o "cyclonedx-json=$sbom"
done

if [ "$found" -eq 0 ]; then
  echo "no release archives found in $dist"
  exit 1
fi
