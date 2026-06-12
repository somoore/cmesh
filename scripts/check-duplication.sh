#!/usr/bin/env sh
set -eu

dirs=$(go list -f '{{.Dir}}' ./...)
out=$(go run github.com/mibk/dupl@v1.1.0 -threshold 120 $dirs || true)
if printf '%s\n' "$out" | grep -Eq '^found [0-9]+ clones:'; then
	echo "duplicate Go code detected:"
	echo "$out"
	exit 1
fi
