#!/usr/bin/env sh
set -eu

at='@'
colon=':'
latest='latest'

if git grep -n "${at}${latest}" -- . ':!scripts/check-supply-chain.sh'; then
	echo "floating module or tool reference found"
	exit 1
fi

if git grep -n "${colon}${latest}" -- . ':!scripts/check-supply-chain.sh'; then
	echo "floating container image reference found"
	exit 1
fi

for workflow in .github/workflows/*.yml .github/workflows/*.yaml; do
  [ -e "$workflow" ] || continue
  awk '
    /^[[:space:]]*uses:/ {
      ref=$0
      sub(/^[[:space:]]*uses:[[:space:]]*/, "", ref)
      gsub(/["'\'']/, "", ref)
      if (ref ~ /^\.\//) next
      if (ref !~ /@[0-9a-f]{40}$/) {
        print FILENAME ":" FNR ": unpinned action reference: " ref
        bad=1
      }
    }
    END { exit bad }
  ' "$workflow"
done
