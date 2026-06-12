#!/usr/bin/env sh
set -eu

go run golang.org/x/vuln/cmd/govulncheck@v1.3.0 ./...
