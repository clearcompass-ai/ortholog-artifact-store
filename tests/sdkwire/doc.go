/*
Package sdkwire holds full HTTP round-trip tests proving the
artifact-store ↔ ortholog-sdk wire-format contract end-to-end.

These tests are NOT under tests/integration/ because they need no
container — the artifact-store serves through net/http/httptest, the
SDK consumes via storage.HTTPRetrievalProvider, and both sides run in
the same Go process.

Why a dedicated package:
  - tests/integration/ requires Docker via its TestMain (testcontainers).
  - The SDK-wire path stays pure-process so it runs in `go test ./...`
    on every developer machine, not just CI runners with Docker.
  - These tests are the canary for any wire-shape drift between the
    artifact-store handlers (api/wire.go) and the SDK's
    storage.HTTPRetrievalProvider — they should fail fast and loud
    without infrastructure-side gates.

Coverage:
  - Resolve credential round-trip with no-expiry (MethodDirect)
  - Resolve credential round-trip with non-nil Expiry (MethodSignedURL)
  - 404 path → storage.ErrContentNotFound
  - JSON wire-shape stability: lowercase keys, RFC3339 expiry, omitempty
*/
package sdkwire
