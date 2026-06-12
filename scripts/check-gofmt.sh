#!/usr/bin/env sh
set -eu

files=$(gofmt -l $(git ls-files '*.go'))
if [ -n "$files" ]; then
  echo "Go files need gofmt:"
  echo "$files"
  exit 1
fi
