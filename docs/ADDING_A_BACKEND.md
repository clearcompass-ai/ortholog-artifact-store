# Adding a New Backend

This is the mechanical checklist for adding a new `BackendProvider`
implementation (for example: Cloudflare R2, Azure Blob Storage, a
future erasure-coded local backend). Follow it in order.

The conformance suite does most of the work. Expect ~90% of your testing
effort to be "wire up the factory"; the other 10% is backend-specific
wire-format and error-classification tests.

---

## 1. Write the source file

Location: `backends/<name>.go`.

Your type must implement `BackendProvider`:

```go
type BackendProvider interface {
    storage.ContentStore // Push, Fetch, Pin, Exists, Delete
    Resolve(cid storage.CID, expiry time.Duration) (*storage.RetrievalCredential, error)
    Healthy() error
}
```

Follow the patterns from `gcs.go`/`s3.go`/`ipfs.go`:

- Pure `net/http`. No vendor SDKs. Keep the dependency surface small.
- A configurable `BaseURL` or `Endpoint` field. Tests need to point the
  backend at `httptest.NewServer` — without an override field this is
  impossible.
- If the backend can produce signed URLs, use an injected signer
  interface (like `GCSURLSigner` or `S3URLSigner`). Never call into a
  vendor SDK directly from the backend — the signer is the seam.
- Error classification: map not-found to `storage.ErrContentNotFound`,
  unsupported-operation to `storage.ErrNotSupported`. Don't wrap them
  in a generic `fmt.Errorf` — callers use `errors.Is` to check.
- `Delete` returns `storage.ErrNotSupported` for write-once stores
  (IPFS model). For deletable stores, actually delete.

---

## 2. Wire the backend into `cmd/artifact-store/main.go`

Add a case to `createBackend`:

```go
case "yourname":
    return backends.NewYourBackend(backends.YourConfig{...}), nil
```

Add `"yourname"` to the valid-backend set in `config/config.go::validate`.

Add a case to `config_test.go::TestLoad_BackendValidation` so the
validation matrix covers your new backend:

```go
{"yourname", "yourname", false},
```

---

## 3. Write an HTTP-mocked test file

Location: `backends/<name>_test.go`.

You have three choices for the fake server:

1. **Reuse an existing `testutil.*Fake`** if your backend speaks a
   compatible protocol (e.g., an S3-compatible backend can reuse
   `S3Fake`).
2. **Add a new fake to `internal/testutil/httpfakes.go`** — follow the
   `GCSFake`/`S3Fake`/`KuboFake` pattern: record every observed request,
   expose error-injection knobs (`PushStatus`, `FetchStatus`, …),
   support preloading objects via a `Put(bucket, key, data)` method.
3. **For very small backends**, inline a one-off `httptest.Server`
   with a hand-rolled handler. Fine for a single test file; if you
   find yourself copy-pasting, promote to `testutil/httpfakes.go`.

At minimum your `<name>_test.go` must cover:

- Push: request shape (method, path, query, headers, body bytes)
- Fetch: returns stored bytes
- Fetch: not-found maps to `storage.ErrContentNotFound`
- Fetch: 5xx returns a non-nil, non-`ErrContentNotFound` error
- Exists: true / false
- Pin: request shape (if applicable) + idempotency
- Delete: happy path + not-supported path (whichever applies)
- Resolve: returns correct `Method` constant + expiry semantics
- Healthy: OK + 5xx error path

The `backends/gcs_test.go` and `backends/s3_test.go` files are the
canonical templates. Copy the structure; change the fake.

---

## 4. Register the backend with the conformance suite

Create a test file that runs `RunBackendConformance` against your backend:

```go
// tests/conformance/suite_yourname_test.go
package conformance_test

import (
    "testing"

    "github.com/clearcompass-ai/ortholog-artifact-store/backends"
    "github.com/clearcompass-ai/ortholog-artifact-store/tests/conformance"
    "github.com/clearcompass-ai/ortholog-sdk/storage"
)

func TestConformance_YourBackend(t *testing.T) {
    conformance.RunBackendConformance(t, "yourname",
        func() backends.BackendProvider {
            return newYourBackendForConformance(t)
        },
        conformance.Capabilities{
            SupportsDelete:        true,         // false for write-once stores
            SupportsExpiry:        true,         // false for permanent public URLs
            ExpectedResolveMethod: storage.MethodSignedURL, // or MethodIPFS / MethodDirect
        },
    )
}
```

`newYourBackendForConformance(t)` constructs a backend pointed at a
fresh `httptest.Server`-based fake, using `t.Cleanup` to tear down
after the test.

Running `go test ./tests/conformance/` must pass.

---

## 5. Fill in the capabilities correctly

`Capabilities` controls which conformance scenarios run:

| Capability | `true` means | `false` means |
|---|---|---|
| `SupportsDelete` | Conformance runs full delete-then-not-exists lifecycle | Conformance asserts `Delete` returns `storage.ErrNotSupported` |
| `SupportsExpiry` | Conformance asserts `Resolve` returns a non-nil `Expiry` and different CIDs produce different URLs | Conformance asserts `Resolve` returns nil `Expiry` |
| `ExpectedResolveMethod` | Required. Conformance asserts `cred.Method` equals exactly this constant. |

Don't guess — read your `Resolve` implementation and set the field
accordingly. If you ever change the `Method` you return, the conformance
test catches it.

---

## 6. Add a fuzz target if your backend has a parser

If your backend parses an untrusted input (e.g., a CID format, a remote
API response), add a fuzz target in `backends/<name>_fuzz_test.go`.
Follow the pattern in `ipfs_cid_fuzz_test.go`:

```go
func FuzzYourParser(f *testing.F) {
    // seed with known-good inputs from testutil.KnownVectors
    // plus adversarial seeds (empty, truncated, garbage)
    f.Fuzz(func(t *testing.T, input string) {
        _, _ = yourParser(input)  // must never panic
    })
}
```

Add the target to the `fuzz` and `fuzz-long` Makefile rules and to
`.github/workflows/fuzz.yml`'s matrix.

---

## 7. Update `TESTING.md`'s layer map

Add a row under Layer 2:

```
| `backends/<name>.go` | `backends/<name>_test.go` | Your custom fake |
```

---

## 8. Update Wave 2/3 if the backend is cloud-hosted

Wave 2 (testcontainers) only makes sense if there's a reference
implementation you can run in a container. The supported set is small:
RustFS for the S3 wire protocol, fake-gcs-server for GCS, Kubo for
IPFS. If your backend has a containerizable reference, add a
`tests/integration/suite_<name>_test.go` consumer.

Wave 3 (real cloud) applies to every cloud-hosted backend. When Wave 3
is implemented, you add `tests/staging/suite_<name>_test.go` with a
dedicated bucket/account and a per-run unique prefix with `t.Cleanup`.

---

## Checklist

Before opening a PR that adds a backend:

- [ ] `backends/<name>.go` — source file
- [ ] `backends/<name>_test.go` — unit tests via httptest server
- [ ] `backends/<name>_fuzz_test.go` — only if you have a parser
- [ ] `cmd/artifact-store/main.go` — case added to `createBackend`
- [ ] `config/config.go` — case added to `validate`
- [ ] `config/config_test.go` — case added to `TestLoad_BackendValidation`
- [ ] `tests/conformance/suite_<name>_test.go` — conformance consumer
- [ ] `internal/testutil/httpfakes.go` — if you added a new fake
- [ ] `docs/TESTING.md` — layer map row added
- [ ] `Makefile` `fuzz` target — if you added a fuzz target
- [ ] `.github/workflows/fuzz.yml` matrix — same
- [ ] `make test-all` passes locally

The conformance suite does most of the work. You bring the wire format,
it brings the contract.
