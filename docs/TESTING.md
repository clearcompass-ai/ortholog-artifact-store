# Testing Strategy

This document explains **where tests live, why they live there, and how
to add new ones**. It is the answer to every "where should my test go?"
question for this repository.

If you're adding code, read the section for the layer you're modifying.
If you're a reviewer, reject PRs whose tests don't match this structure.
If you're onboarding, read it top-to-bottom once.

---

## The five-layer model

We structure testing as a pyramid. Each layer catches a different class
of bug. Each layer has a different cost. Investment compounds as you
go up.

```
                        ┌─────────────────┐
                        │  Prod canaries  │   60s cadence — deployed service
                        ├─────────────────┤   (not in this repo)
                        │  Staging E2E    │   Wave 3 — nightly, real cloud
                        ├─────────────────┤   Filebase + real GCS + real AWS
                        │  Ephemeral E2E  │   Wave 2 — per-PR, testcontainers
                        ├─────────────────┤   RustFS + fake-gcs + Kubo
                        │  HTTP-mocked    │   Wave 1 — per-save, httptest
                        ├─────────────────┤
                        │  Unit + table   │   Wave 1 — per-save
                        └─────────────────┘
```

**Wave 1 is fully implemented. Wave 2 and Wave 3 are planned additions that
follow the same structural patterns — see "How Wave 2/3 fit in" below.**

---

## Wave 1: what's in place today

### Layer 1 — Unit + table-driven (per-save, sub-second)

Every source file has a `_test.go` sibling. Tests exercise the source
file's public surface with small, focused, fast cases.

| Source file | Test file | What it tests |
|---|---|---|
| `api/server.go` | `api/server_test.go` | NewMux wiring, nil-logger fallback, 404/405 paths |
| `api/push.go` | `api/push_test.go` | Size limits, digest verification, **audit log event/reason assertions** |
| `api/push.go` | `api/push_token_test.go` | **AS-2** token policy matrix (off/optional/required), 401 paths, audit reasons |
| `api/token.go` | `api/token_test.go` | **AS-2** Ed25519 verifier: happy path, bad signature, expired, CID/size mismatch, malformed |
| `api/fetch.go` | `api/fetch_test.go` | 200/404/500 + cache headers |
| `api/resolve.go` | `api/resolve_test.go` | `?expiry=` parsing matrix, method dispatch |
| `api/pin.go` | `api/pin_test.go` | Happy path + not-found + backend error |
| `api/exists.go` | `api/exists_test.go` | HEAD semantics, empty body |
| `api/delete.go` | `api/delete_test.go` | Success, `ErrNotSupported` → 501 |
| `api/health.go` | `api/health_test.go` | Healthy/unhealthy JSON shape |
| `api/helpers.go` | `api/helpers_test.go` | `parseCIDFromPath`, `writeError` JSON validity |
| `config/config.go` | `config/config_test.go` | Env matrix, validation, defaults, **AS-2 token policy validation** |
| `cmd/artifact-store/main.go` | `cmd/artifact-store/main_test.go` | **AS-1** watchdog: one-shot in dev, periodic in production, clean shutdown |
| `cmd/artifact-store/token.go` | `cmd/artifact-store/token_test.go` | **AS-2** key loading (PEM/hex/base64, inline/file), policy decision tree |

**Run:** `make test` — completes in about 5 seconds.

### Layer 2 — HTTP-mocked backends (per-save, ~1 second)

Each backend is tested against an `httptest.Server` fake of the real
remote API. This catches wire-format bugs — wrong URLs, missing headers,
malformed bodies, error classification — without requiring Docker or
network access.

| Source | Test | Fake server |
|---|---|---|
| `backends/gcs.go` | `backends/gcs_test.go` | `testutil.GCSFake` (GCS JSON API) |
| `backends/rustfs.go` | `backends/rustfs_test.go` | `testutil.S3Fake` (S3 wire protocol, path-style) |
| `backends/ipfs.go` | `backends/ipfs_test.go` | `testutil.KuboFake` (Kubo RPC API) |
| `backends/mirrored.go` | `backends/mirrored_test.go` | `scriptedBackend` with per-call error injection |
| `backends/ipfs.go` | `backends/ipfs_cid_fuzz_test.go` | Go fuzz (10-30s in CI) |

The HTTP fakes live in `internal/testutil/httpfakes.go`. They record
every request so tests can assert on method, path, query, headers, and
body. They expose error-injection knobs (`PushStatus`, `FetchStatus`,
etc.) so tests can simulate 500s, 404s, 403s without needing a real
failing server.

### Layer 2b — Backend conformance suite (the spine)

`tests/conformance/` is a test suite that every backend is expected to
pass. It's the **contract** all backends share. One suite, many backends.

```go
// First consumer: InMemoryBackend (Wave 1)
conformance.RunBackendConformance(t, "inmemory",
    func() backends.BackendProvider {
        return backends.NewInMemoryBackend()
    },
    conformance.Capabilities{
        SupportsDelete:        true,
        SupportsExpiry:        false,
        ExpectedResolveMethod: storage.MethodDirect,
    },
)
```

The suite runs five scenario categories: `Lifecycle`, `Resolve`, `Errors`,
`Concurrent`, `Integrity`. Adding a backend requires adding one factory
and it gets ~30 tests for free.

**Wave 2 adds more consumers:** RustFS, fake-gcs-server, Kubo. Same suite,
same scenarios, run against real-protocol implementations in Docker.

**Wave 3 adds even more:** Filebase (IPFS + S3), real AWS, real GCS.
Same suite, same scenarios, run against production cloud.

### Shared test infrastructure

`internal/testutil/` is the toolkit every test file reaches for:

| File | Purpose |
|---|---|
| `slogcapture.go` | Capturing `slog.Handler` with `AssertContains(level, msg, attrs)` — enables audit-log assertions (push handler's 3.6 audit trail) |
| `httpfakes.go` | `GCSFake`, `S3Fake`, `KuboFake` — httptest servers that mimic the real APIs |
| `goleak.go` | `RunWithGoleak(m)` — one-liner for `TestMain` that fails on goroutine leaks |
| `cidvectors.go` | Known SHA-256 vectors for regression-proofing CID computation |
| `hash.go` | Internal SHA-256 helper shared by the above |

`RunWithGoleak` is plumbed into `api/main_test.go`, `backends/backend_test.go`,
and `tests/conformance/suite_inmemory_test.go`. Any test that leaks a
goroutine fails the whole binary — enforced across the entire suite.

### Enforcement gates

| Gate | Where | What it does |
|---|---|---|
| `-race` | Makefile, CI | Mandatory on every test run. Catches data races. |
| `goleak` | `TestMain` in 4 packages | Fails if any goroutine survives tests. (`api`, `backends`, `tests/conformance`, and `cmd/artifact-store` for the AS-1 watchdog.) |
| Coverage 80% floor | `scripts/coverage-gate.sh` | CI fails if any non-cmd package drops below 80%. |
| Lint | `.golangci.yml` | vet, staticcheck, gosec, ineffassign, unused. |
| Fuzz smoke | CI `fuzz-smoke` job | Each fuzz target runs 10s per PR. |
| Weekly fuzz | `.github/workflows/fuzz.yml` | 5m per target; auto-PR on crash. |
| Weekly flake-detect | `.github/workflows/flake-detect.yml` | Suite runs 50×; any failure = issue. |

### Audit-log schema (AS-3)

Every push rejection emits a WARN-level structured log record. The schema
is **stable** — tests assert on these exact strings, and downstream log
pipelines can alert on them without fuzzy matching. If a new rejection
reason is added, the enum below and the assertions in `push_test.go`
and `push_token_test.go` must be updated together.

```
event  = "artifact.push.rejected"      // always this value
reason = one of:
    missing_cid_header       invalid_cid_header       read_body_error
    size_exceeded            cid_mismatch             backend_error
    token_required_missing   token_invalid            token_expired
    token_cid_mismatch       token_size_mismatch      token_malformed
```

Context attributes (not all present on every record):

| Attr | Type | Present when |
|---|---|---|
| `claimed_cid` | string | always (may be empty on `missing_cid_header`) |
| `remote_addr` | string | always |
| `received_size` | int64 | always (zero before body read) |
| `max_body_size` | int64 | on `size_exceeded` |
| `computed_digest` | string (hex) | on `cid_mismatch` |
| `claimed_digest` | string (hex) | on `cid_mismatch` |
| `operator_token_kid` | string | on `cid_mismatch` when a token was present |
| `error` | string | on `backend_error`, `token_*` |

Tests assert on `event`, `reason`, and the most relevant context attrs via
`testutil.SlogCapture.AssertContains`. Any change to attribute names is a
breaking change for log consumers and must be called out in the PR.

---

## How to add a test

### Adding a test for an existing source file

Test lives in `<source_file>_test.go`, same package. Follow the patterns
already in that file.

If your test needs audit-log assertions (new error paths in push, new
warning paths elsewhere), use `testutil.NewSlogCapture()`:

```go
cap := testutil.NewSlogCapture()
h := &PushHandler{ ..., logger: cap.Logger() }
// ... exercise h ...
cap.AssertContains(t, slog.LevelWarn, "substring of message",
    map[string]any{"key": expectedValue})
```

If your test launches a goroutine, verify it's cleaned up before the
test ends. `goleak` in TestMain will fail the binary otherwise.

### Adding a new handler (new `api/foo.go`)

1. Write `api/foo.go` — the handler.
2. Write `api/foo_test.go` — at minimum: happy path, malformed CID, backend
   error, and any handler-specific error paths.
3. Wire the handler into `NewMux` in `api/server.go`.
4. Add a case to the routing table in `api/server_test.go::TestMux_WiresAllRoutes`.

### Adding a new backend

See `docs/ADDING_A_BACKEND.md` — this is a common enough activity to
have its own checklist.

### Adding a test that must not be run in per-PR CI

If the test is slow, requires external resources, or is probabilistic,
gate it with a build tag:

```go
//go:build integration

package integration
```

Wave 2 tests use `//go:build integration`. Wave 3 tests use
`//go:build staging`. The per-PR CI runs neither.

**Do not use `t.Skip(...)` gated on environment variables.** That pattern
compiles the test into the binary but silently skips it, creating the
illusion of coverage. Build tags make the decision mechanical: either
the file is in the binary or it isn't.

---

## Anti-patterns we've removed (and why)

These are patterns that looked like coverage but weren't. They are now
forbidden — reject PRs that reintroduce them.

### "Is the constructor non-nil?"

```go
// ❌ BEFORE
func TestGCS_Push(t *testing.T) {
    b := newGCSTestBackend()
    if b == nil { t.Fatal("constructor returned nil") }
}
```

A Go constructor literally cannot return nil unless the code explicitly
does `return nil` — the test verifies nothing. Rewritten as:

```go
// ✅ AFTER — actually exercises Push through an httptest server
func TestGCS_Push_SendsExactRequestShape(t *testing.T) {
    fake := testutil.NewGCSFake(t)
    b := newGCSBackendWithFake(t, fake)
    cid := storage.Compute(data)
    require.NoError(t, b.Push(cid, data))
    // ... assert method, path, query, headers, body ...
}
```

### `t.Skip` on missing env vars

```go
// ❌ BEFORE
func TestSmoke_GCS(t *testing.T) {
    if os.Getenv("GCS_BUCKET") == "" {
        t.Skip("GCS_BUCKET not set")
    }
}
```

The test is always compiled, always "skipped" in local dev, never
actually runs. It looks like coverage to reviewers and the test summary.

```go
// ✅ AFTER — build-tag exclusion
//go:build staging

package staging
```

The test file is only compiled when `go test -tags=staging`. If the tag
isn't set, the test doesn't exist. Obvious.

### Tests with `t.Log` instead of assertions

```go
// ❌ BEFORE
if err == nil {
    t.Log("GCS healthy (unexpected — no real GCS)")
}
```

`t.Log` prints output but doesn't fail. The test passes regardless of
`err`'s value.

```go
// ✅ AFTER
if err == nil {
    t.Fatal("Healthy with no real GCS should error; got nil")
}
```

---

## Wave 2: testcontainer-backed integration (implemented)

Wave 2 runs the same conformance suite from Wave 1 against real protocol
implementations in Docker containers. This catches wire-format divergences
between our HTTP model and the real vendor APIs that Wave 1's httptest
fakes cannot see.

### Files

```
tests/integration/
├── doc.go                          # purpose, gating, prerequisites
├── main_test.go                    # TestMain: verifies Docker is reachable
├── helpers_test.go                 # randomPrefix for parallel isolation
├── containers/
│   ├── rustfs.go                   # RustFS container (S3 wire protocol)
│   ├── fakegcs.go                  # fake-gcs-server container (GCS)
│   ├── kubo.go                     # Kubo container (IPFS)
│   ├── util.go                     # image-override env, HTTP helpers
│   └── sigv4.go                    # minimal SigV4 signer (test-only)
├── suite_rustfs_test.go            # RunBackendConformance(t, "rustfs", ...)
├── suite_fakegcs_test.go           # RunBackendConformance(t, "fakegcs", ...)
├── suite_kubo_test.go              # RunBackendConformance(t, "kubo", ...)
└── suite_mirrored_test.go          # MirroredStore across RustFS+GCS containers
```

Every file carries `//go:build integration`. `go test ./...` (the Wave 1
flow) compiles none of this and starts no containers.

### What Wave 2 uniquely validates

| Risk | Wave 1 HTTP mock | Wave 2 container |
|---|---|---|
| Wrong URL shape | ✓ | ✓ |
| Wrong method/headers | ✓ | ✓ |
| Error code classification | our model | real protocol |
| SigV4 signature format | n/a (mock accepts anything) | ✓ real S3-compatible server |
| IPFS multihash encoding drift | ✓ round-trip in our code | ✓ **real Kubo's output** |
| Kubo RPC API contract changes | n/a | ✓ catches version-to-version breaks |
| Cross-protocol mirroring | scripted double | ✓ real heterogeneous backends |

The most important test in Wave 2 is `TestKubo_CIDDigestMatchesSDK` —
it asserts that the SHA-256 digest extracted from a real Kubo CIDv1
response matches the SDK's digest for the same bytes, across payload
sizes. If the multihash encoding drifts (alphabet, multibase, multicodec,
block-size logic), this test catches it before production.

### Running Wave 2

```bash
make test-integration        # requires Docker daemon running
```

Or directly:

```bash
go test -race -count=1 -tags=integration ./tests/integration/...
```

Runtime: ~90 seconds total. Container startup dominates; each test reuses
a single container across its scenarios via `t.Cleanup`.

### CI

`.github/workflows/integration.yml` runs the suite:
- Nightly at 05:00 UTC (scheduled)
- On every push to `main`
- On manual `workflow_dispatch`

Not per-PR — Wave 1 handles that. Wave 2 is the post-merge safety net
for protocol-level regressions.

### Image pinning

`RUSTFS_IMAGE`, `FAKEGCS_IMAGE`, `KUBO_IMAGE` env vars override the
defaults. Local dev defaults to `:latest` for convenience; CI pins to
specific released digests in `.github/workflows/integration.yml` so
container updates are deliberate commits, not silent drift.

---

## Wave 3: real cloud staging (implemented)

Wave 3 runs the conformance suite against the two cloud-coupled
backends in scope: real GCS and Filebase IPFS pinning. This catches
vendor-specific production quirks that containerized emulators miss
— GCS V4 signed-URL compatibility, Filebase IPFS propagation timing.

The S3-protocol path is **not** in Wave 3. It is exercised in Wave 2
against a containerized RustFS, which is the supported S3-protocol
implementation. Real-cloud S3 vendors (AWS, Wasabi, R2, Filebase-S3)
are intentionally not tested here.

### Files

```
tests/staging/
├── doc.go                             # purpose, credential policy, cost model
├── main_test.go                       # STAGING_ENABLED gate + per-vendor credential validation
├── helpers_test.go                    # randomPrefix + operation counter
├── http_helpers_test.go               # fetchURLBytes + eventual-consistency retry
├── suite_gcs_test.go                  # real GCS conformance + V4 signed URL fetch
└── suite_filebase_ipfs_test.go        # Filebase IPFS RPC + CID digest property + gateway fetch

internal/signers/                       # Shared real signers (no build tag)
├── sigv4.go                            # Full SigV4 with PresignGetObject (used by RustFS)
├── gcs.go                              # GCS V4 RSA-SHA256 service-account signer
└── s3_presigner.go                     # Bound S3-protocol presigner wrapper
```

Every test file carries `//go:build staging`. Neither Wave 1 nor Wave 2
compiles this package.

### What Wave 3 uniquely validates

| Risk | Wave 1 mock | Wave 2 container | Wave 3 real cloud |
|---|---|---|---|
| Wire shape | ✓ | ✓ | ✓ |
| Vendor error classification | our model | reference impl | **production impl** |
| Real GCS V4 signed URL format | no | no | ✓ |
| Real Kubo CID from Filebase | no | unauthenticated Kubo | ✓ **with auth, real network** |
| Eventual consistency behavior | no | no | ✓ (IPFS gateway propagation) |

The most valuable Wave 3 tests:

- `TestGCS_SignedURLIsFetchable` — proves our V4 RSA-SHA256 signing
  matches GCS's expected format
- `TestFilebase_IPFS_CIDDigestMatchesSDK` — the production analogue
  of Wave 2's Kubo test: Filebase's IPFS implementation returns CIDs
  whose extracted digests match our SDK's computation
- `TestFilebase_IPFS_GatewayURL_Fetchable` — content pinned via RPC
  actually becomes fetchable at the public gateway (with backoff for
  eventual consistency)

### Gating

Wave 3 requires **two** concurrent gates to run:

1. `-tags=staging` on the `go test` invocation (build tag)
2. `STAGING_ENABLED=1` in the environment (second gate)

The second gate exists because the build tag alone doesn't protect
against an accidental `go test -tags=staging ./...` in the wrong shell.
Both the `Makefile` target and `main_test.go` check for it and fail
loud if it's missing. CI sets it explicitly; developer shells never
should.

### Credentials

Per-vendor credential sets are validated by `main_test.go` at startup.
A partial set (some vars set, others missing) is a fatal misconfiguration
— the test binary exits with status 2 and names the missing vars.
Absent ALL vars for a vendor, that vendor's tests skip cleanly with a
`t.Logf` trail so the skip shows up in the CI summary.

| Vendor | Env vars |
|---|---|
| GCS | `STAGING_GCS_BUCKET`, `STAGING_GCS_SERVICE_ACCOUNT_JSON` (path), `STAGING_GCS_ACCESS_TOKEN` (from `gcloud auth print-access-token`, minted by CI) |
| Filebase IPFS | `STAGING_FILEBASE_IPFS_TOKEN` |

### Cost control

- Dedicated accounts per vendor, scoped via IAM to the test buckets
- Bucket lifecycle rules delete objects under `staging/` after 24 hours
- Each test run uses a unique key prefix (`staging/{TestName}/{random}/`)
- Budget alerts at $20/month per account, vendor-side
- `recordOp(t)` instruments each cloud call; a summary test prints the
  total at the end of each run so cost-regression drift is visible in CI logs

### Running Wave 3

```bash
export STAGING_ENABLED=1
# ...plus every STAGING_* env var for the vendors you want to exercise
make test-staging
```

In CI: `.github/workflows/staging.yml` runs nightly at 04:00 UTC. It
injects the secrets, mints a short-lived GCS access token via
`google-github-actions/auth`, and calls `make test-staging`. On failure,
it dumps the remaining objects under `staging/` to the CI log for
post-mortem.

---

## v7.75 alignment gate

`make audit-v775-consumer` is the per-release gate that verifies the
artifact store correctly consumes SDK v7.75. It runs five checks
without Docker or cloud credentials:

1. `go build ./...` — module + SDK pin compiles
2. `go vet ./...` — toolchain alignment
3. `go vet -tags=integration ./tests/integration/...` — Wave 2 builds
4. `go vet -tags=staging ./tests/staging/...` — Wave 3 builds
5. `go test -race -count=1 ./...` — Wave 1 unit + conformance

Step 5 includes the v7.75-specific test surface:

| Test file | What it pins |
|---|---|
| `api/push_algorithm_agile_test.go` | Part 2: push uses `cid.Verify` (algorithm-agile, not hard-coded SHA-256) |
| `backends/ipfs_algorithm_guard_test.go` | Part 3: IPFS rejects non-SHA-256 CIDs at every method, fail-closed before any HTTP |
| `tests/conformance/scenarios_cid_wire.go` | Part 3: `CID.Bytes()` wire-form preserved (algorithm tag survives) across every backend |
| `api/token_test.go` `TestToken_Verify_KidDispatch_*` | Part 4: kid-keyed verifier dispatches correctly across rotation windows |
| `api/push_token_test.go` `TestPush_TokenRotationWindow_*` | Part 4: operator key rotation honored at the push handler with `token_unknown_kid` audit reason |
| `cmd/artifact-store/token_test.go` | Part 4: `ARTIFACT_OPERATOR_PUBKEYS` and `ARTIFACT_OPERATOR_PUBKEYS_DIR` loaders parse PEM/hex/base64 |

The gate runs in CI on every PR (see `.github/workflows/ci.yml`'s
`v7.75 alignment gate` step) so an alignment regression surfaces as a
distinct red signal, not buried inside a generic test failure.

---

## Running tests locally

```bash
make test                   # Wave 1 — 5 seconds
make test-verbose           # same with -v
make test-integration       # Wave 2 — 90 seconds, requires Docker
make test-staging           # Wave 3 — up to 5 minutes, requires STAGING_ENABLED=1 + vendor credentials
make audit-v775-consumer    # v7.75 SDK alignment gate (build + vet + Wave 1 tests)
make coverage               # produces coverage.html
make lint                   # go vet + staticcheck
make fuzz                   # 30s per fuzz target
make flake                  # 50 iterations; detects flakes
make test-all         # lint + test + coverage-gate
```

If a test fails:

1. Re-run with `-v` to see the full output: `go test -race -v ./api -run=TestPush_DigestMismatch`
2. If it's a goroutine leak, the failure message names the top function of the leaked goroutine. Look for missing `t.Cleanup`, missing `Close()`, or a goroutine that doesn't respect context cancellation.
3. If it's a `-race` failure, the message includes both stack traces that conflict.
4. If it's a flake (fails 1/20 runs), **don't retry until it passes**. File an issue. Tests must be deterministic.

---

## What this strategy optimizes for

1. **Finding the right place to add a test.** The structure above answers "where does my test go?" mechanically.
2. **Fast feedback.** Wave 1 completes in seconds. Wave 2 in minutes. Wave 3 off-PR.
3. **Trustworthy signals.** `-race`, `goleak`, coverage gate, flake detection, fuzzing — every one is a gate, not a suggestion. If one fires, there's a real problem.
4. **Growth without refactoring.** New backend → one factory. New scenario → one function in the conformance suite. New wave → new directory.

Exceptional testing isn't glamorous. It's the absence of confusion,
compounded across every engineer who touches the code.
