#!/usr/bin/env sh
set -eu

base_sha=${BASE_SHA:-}
head_sha=${HEAD_SHA:-}

if [ -z "$base_sha" ] || [ -z "$head_sha" ]; then
  echo false
  exit 0
fi

case "$base_sha" in
  0000000000000000000000000000000000000000)
    if git rev-parse -q --verify "$head_sha^" >/dev/null; then
      base_sha="$head_sha^"
    else
      echo false
      exit 0
    fi
    ;;
esac

changed_files=$(git diff --name-only "$base_sha" "$head_sha")

if [ -z "$changed_files" ]; then
  echo false
  exit 0
fi

tmp_changed=$(mktemp)
trap 'rm -f "$tmp_changed"' EXIT
printf '%s\n' "$changed_files" > "$tmp_changed"

while IFS= read -r path; do
  case "$path" in
    *.md) ;;
    *)
      echo false
      exit 0
      ;;
  esac
done < "$tmp_changed"

echo true
