#!/usr/bin/env sh
set -eu

go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7 -config-file .github/actionlint.yaml
