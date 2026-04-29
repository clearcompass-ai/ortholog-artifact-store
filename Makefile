# ============================================================================
# ortholog-artifact-store — Makefile
#
# Wave 1 targets. Wave 2 adds test-integration (testcontainers). Wave 3
# adds test-staging (Filebase + AWS + GCS). The Makefile shape is stable
# across waves — new targets, no renames.
#
# Conventions:
#   - Every test target uses -race. The race detector catches bugs that
#     only appear under parallel access. Not optional.
#   - Every test target uses -count=1 to defeat Go's test result cache.
#     Test results are cheap; stale caches are expensive.
#   - Coverage is a per-PR gate, not a nightly report. The gate fails CI.
# ============================================================================

GO ?= go
PKGS := ./...
COVERAGE_OUT := coverage.out
COVERAGE_HTML := coverage.html

# This repo is a self-contained module. A parent-directory go.work
# that doesn't list it (common when the repo lives next to siblings
# under ~/workspace) makes `go test`/`go vet ./...` fail with
# "directory prefix ... does not contain modules listed in go.work".
# Setting GOWORK=off Make-wide sidesteps the collision without
# touching the user's personal workspace file.
export GOWORK := off

.PHONY: help
help: ## Show this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n\nTargets:\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

# ─── Core test targets ───────────────────────────────────────────────

.PHONY: test
test: ## Run unit and HTTP-mocked tests with -race (Wave 1).
	$(GO) test -race -count=1 -timeout=60s $(PKGS)

.PHONY: test-integration
test-integration: ## Wave 2: run testcontainer-backed integration tests. Requires Docker.
	@command -v docker >/dev/null 2>&1 || { echo "docker not found in PATH"; exit 1; }
	@docker info >/dev/null 2>&1 || { echo "Docker daemon not reachable; start Docker first"; exit 1; }
	$(GO) test -race -count=1 -timeout=10m -tags=integration ./tests/integration/...

.PHONY: test-wave2
test-wave2: test-integration ## Alias: Wave 2 integration tests.

.PHONY: test-staging
test-staging: ## Wave 3: real cloud against Filebase + AWS + GCS. Nightly only.
	@if [ "$$STAGING_ENABLED" != "1" ]; then \
		echo "refusing to run staging tests without STAGING_ENABLED=1"; \
		echo "this is a deliberate second gate on top of -tags=staging"; \
		echo "to prevent accidental real-cloud API hits from a dev shell"; \
		exit 1; \
	fi
	$(GO) test -race -count=1 -timeout=30m -tags=staging -v ./tests/staging/...

.PHONY: test-wave3
test-wave3: test-staging ## Alias: Wave 3 real-cloud tests.

.PHONY: test-scale-gcs
test-scale-gcs: ## Standalone load test: SCALE_N=10000 mixed-size pushes to real GCS. Requires STAGING_GCS_* env.
	@bash scripts/run-scale-gcs.sh

.PHONY: test-short
test-short: ## Run only short tests (skip slow integrity sizes).
	$(GO) test -race -count=1 -short -timeout=30s $(PKGS)

.PHONY: test-verbose
test-verbose: ## Verbose test output.
	$(GO) test -race -count=1 -v -timeout=60s $(PKGS)

# ─── Coverage ────────────────────────────────────────────────────────

.PHONY: coverage
coverage: ## Produce coverage profile and HTML report.
	$(GO) test -race -count=1 -coverprofile=$(COVERAGE_OUT) $(PKGS)
	$(GO) tool cover -html=$(COVERAGE_OUT) -o $(COVERAGE_HTML)
	@echo "HTML report: $(COVERAGE_HTML)"
	@$(GO) tool cover -func=$(COVERAGE_OUT) | tail -n 1

.PHONY: coverage-gate
coverage-gate: ## Fail if coverage drops below 80% in any non-cmd package.
	@$(GO) test -race -count=1 -coverprofile=$(COVERAGE_OUT) $(PKGS) > /dev/null
	@bash scripts/coverage-gate.sh $(COVERAGE_OUT) 80

# ─── Lint ────────────────────────────────────────────────────────────

.PHONY: lint
lint: ## Run go vet + staticcheck (installed on demand).
	$(GO) vet $(PKGS)
	@command -v staticcheck >/dev/null 2>&1 || $(GO) install honnef.co/go/tools/cmd/staticcheck@latest
	staticcheck $(PKGS)

.PHONY: lint-all
lint-all: lint ## lint + golangci-lint for everything else.
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "golangci-lint not installed; see https://golangci-lint.run/usage/install/"; exit 1; \
	}
	golangci-lint run --timeout=2m

# ─── Fuzz (no targets currently) ─────────────────────────────────────
#
# The pre-v7.75 fuzz targets (FuzzExtractDigestFromIPFSCID,
# FuzzSDKCIDToIPFSPath_RoundTrip, FuzzParseCID) lived in the IPFS
# backend's CID parser. IPFS is no longer a supported backend kind;
# the targets and the weekly fuzz workflow (.github/workflows/fuzz.yml)
# are gone with it.
#
# When a future backend or helper introduces a new parser worth
# fuzzing, re-add a `fuzz` target here pointing at the relevant
# Fuzz<Name> function and re-add the weekly workflow that mirrors it.

# ─── Flake detection ─────────────────────────────────────────────────

.PHONY: flake
flake: ## Run full test suite 50x; report any failure. Expects 0 flakes.
	@bash scripts/flake-detector.sh

# ─── Convenience ─────────────────────────────────────────────────────

.PHONY: test-all
test-all: lint test coverage-gate ## Everything that runs in per-PR CI.

# ─── v7.75 SDK alignment audit gate ──────────────────────────────────
#
# audit-v775-consumer is the per-release alignment gate. It runs every
# verification the artifact-store can do as a v7.75 SDK consumer
# without requiring Docker or cloud credentials:
#
#   1. go build ./...                         — module + SDK pin compiles
#   2. go vet ./...                           — toolchain alignment
#   3. go vet -tags=integration ./tests/...   — Wave 2 builds even
#                                                without Docker
#   4. go vet -tags=staging     ./tests/...   — Wave 3 builds without
#                                                cloud creds
#   5. go test -race -count=1 ./...           — Wave 1 unit + conformance
#      includes:
#        - api/push_algorithm_agile_test.go   Part 2 (cid.Verify
#                                              algorithm-agile)
#        - tests/conformance/scenarios_cid_   Part 3 (CID.Bytes() wire
#          wire.go                             form across every
#                                              object-store backend)
#        - api/token_test.go kid-dispatch     Part 4 (kid-keyed verifier
#                                              + SDK signatures.Verify
#                                              Ed25519)
#        - api/push_token_test.go rotation    Part 4 (operator key
#                                              rotation window)
#
# A passing make audit-v775-consumer is the CI signal that the
# artifact-store correctly consumes SDK v7.75 across every layer.
.PHONY: audit-v775-consumer
audit-v775-consumer: ## v7.75 alignment gate (build + vet + Wave 1 tests; no Docker required).
	@echo "==> v7.75 alignment gate"
	@echo "--> go build ./..."
	@$(GO) build ./...
	@echo "--> go vet ./..."
	@$(GO) vet ./...
	@echo "--> go vet -tags=integration ./tests/integration/..."
	@$(GO) vet -tags=integration ./tests/integration/...
	@echo "--> go vet -tags=staging ./tests/staging/..."
	@$(GO) vet -tags=staging ./tests/staging/...
	@echo "--> go test -race -count=1 ./..."
	@$(GO) test -race -count=1 -timeout=120s $(PKGS)
	@echo
	@echo "==> v7.75 alignment gate PASSED"

.PHONY: clean
clean: ## Remove coverage artifacts.
	rm -f $(COVERAGE_OUT) $(COVERAGE_HTML)

.PHONY: tidy
tidy: ## go mod tidy.
	$(GO) mod tidy

.DEFAULT_GOAL := help
