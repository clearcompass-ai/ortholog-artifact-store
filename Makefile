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

# ─── Fuzz ────────────────────────────────────────────────────────────

.PHONY: fuzz
fuzz: ## Run fuzz targets for 30 seconds each (CI setting).
	$(GO) test -fuzz=FuzzExtractDigestFromIPFSCID -fuzztime=30s ./backends
	$(GO) test -fuzz=FuzzSDKCIDToIPFSPath_RoundTrip -fuzztime=30s ./backends
	$(GO) test -fuzz=FuzzParseCID -fuzztime=30s ./backends

.PHONY: fuzz-long
fuzz-long: ## Run fuzz targets for 5 minutes each (weekly CI setting).
	$(GO) test -fuzz=FuzzExtractDigestFromIPFSCID -fuzztime=5m ./backends
	$(GO) test -fuzz=FuzzSDKCIDToIPFSPath_RoundTrip -fuzztime=5m ./backends
	$(GO) test -fuzz=FuzzParseCID -fuzztime=5m ./backends

# ─── Flake detection ─────────────────────────────────────────────────

.PHONY: flake
flake: ## Run full test suite 50x; report any failure. Expects 0 flakes.
	@bash scripts/flake-detector.sh

# ─── Convenience ─────────────────────────────────────────────────────

.PHONY: test-all
test-all: lint test coverage-gate ## Everything that runs in per-PR CI.

.PHONY: clean
clean: ## Remove coverage artifacts.
	rm -f $(COVERAGE_OUT) $(COVERAGE_HTML)

.PHONY: tidy
tidy: ## go mod tidy.
	$(GO) mod tidy

.DEFAULT_GOAL := help
