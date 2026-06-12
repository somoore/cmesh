#!/usr/bin/env sh
set -eu

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

cp go.mod "$tmpdir/go.mod.before"
cp go.sum "$tmpdir/go.sum.before"

go mod tidy

if ! cmp -s go.mod "$tmpdir/go.mod.before" || ! cmp -s go.sum "$tmpdir/go.sum.before"; then
  echo "go.mod or go.sum changed after go mod tidy"
  git diff -- go.mod go.sum
  cp "$tmpdir/go.mod.before" go.mod
  cp "$tmpdir/go.sum.before" go.sum
  exit 1
fi

go list ./... >/dev/null

nested=$(git ls-files '**/go.mod' | grep -v '^go.mod$' || true)
if [ -n "$nested" ]; then
  echo "unexpected nested Go modules:"
  echo "$nested"
  exit 1
fi
