APP := cmesh
GO ?= go
DIST_DIR ?= dist
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
COSIGN_VERSION ?= v2.6.3
SYFT_VERSION ?= v1.45.1
LDFLAGS := -s -w -X cmesh/internal/version.Version=$(VERSION) -X cmesh/internal/version.Commit=$(COMMIT) -X cmesh/internal/version.Date=$(DATE)

.PHONY: test precommit unit fmt fmt-check mod-check supply-chain dup vuln build sbom release-notes sign deploy clean install-hooks

test: fmt-check mod-check supply-chain dup vuln unit

precommit: fmt-check mod-check supply-chain dup unit

unit:
	$(GO) test ./...

fmt:
	$(GO) fmt ./...

fmt-check:
	@scripts/check-gofmt.sh

mod-check:
	@scripts/check-go-modules.sh

supply-chain:
	@scripts/check-supply-chain.sh

dup:
	@scripts/check-duplication.sh

vuln:
	@scripts/check-vuln.sh

build:
	@scripts/build-release.sh "$(APP)" "$(VERSION)" "$(COMMIT)" "$(DATE)" "$(DIST_DIR)"

sbom:
	@scripts/generate-sboms.sh "$(DIST_DIR)" "$(SYFT_VERSION)"

release-notes:
	@scripts/generate-release-notes.sh "$(VERSION)" "$(DIST_DIR)"

sign:
	@scripts/sign-release.sh "$(DIST_DIR)" "$(COSIGN_VERSION)"

deploy: test
	@if [ -z "$(VERSION)" ] || ! printf '%s' "$(VERSION)" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$$'; then \
		echo "VERSION must be a semver tag such as v0.1.0"; exit 1; \
	fi
	@git diff --quiet || { echo "working tree has unstaged changes"; exit 1; }
	@git diff --cached --quiet || { echo "index has staged changes"; exit 1; }
	git tag -s "$(VERSION)" -m "$(APP) $(VERSION)"
	git push origin "$(VERSION)"

clean:
	rm -rf "$(DIST_DIR)"

install-hooks:
	@scripts/install-hooks.sh
