#!/usr/bin/env sh
set -eu

root=$(git rev-parse --show-toplevel)
mkdir -p "$root/.git/hooks"
cp "$root/scripts/hooks/pre-commit" "$root/.git/hooks/pre-commit"
chmod +x "$root/.git/hooks/pre-commit"
echo "installed pre-commit hook"
