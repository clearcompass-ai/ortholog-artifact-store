# Adding a New Object Store Backend

The artifact store supports exactly one kind of backend: a remote
object store with put/get/head/delete semantics, signed-URL retrieval,
and arbitrary opaque bytes per object. The currently shipped set is
GCS, RustFS (the S3-protocol implementation), and an in-memory
reference. Anything new must satisfy `BackendProvider` and pass the
conformance suite.

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

Follow the patterns from `gcs.go` / `rustfs.go`:

- Pure `net/http`. No vendor SDKs. Keep the dependency surface small.
- A configurable `Endpoint` field. Tests need to point the backend at
  `httptest.NewServer` — without an override field this is impossible.
- If the backend produces signed URLs, use an injected signer
  interface (like `GCSURLSigner` or `RustFSURLSigner`). Never call
  into a vendor SDK directly from the backend — the signer is the
  seam, and the production signer lives in `internal/signers/`.
- Error classification: map not-found to `storage.ErrContentNotFound`.
  Don't wrap them in a generic `fmt.Errorf` — callers use `errors.Is`
  to check.
- All object-store backends support Delete. Returning
  `storage.ErrNotSupported` from Delete is no longer accepted (it was
  the IPFS-era convention; IPFS is no longer a supported backend kind).

---

## 2. Wire the backend into `cmd/artifact-store/main.go`

Add a case to `createBackend`:

```go
case "yourname":
    return backends.NewYourBackend(backends.YourConfig{...}), nil
```

Add `"yourname"` to the valid-backend set in `config/config.go::validate`
and to the mirror set if applicable.

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
   `GCSFake` / `S3Fake` pattern: record every observed request,
   expose error-injection knobs (`PushStatus`, `FetchStatus`, …),
   support preloading objects via a `Put(bucket, key, data)` method.
   Both fakes have full coverage in `internal/testutil/httpfakes_test.go`
   — match that bar for any new fake you add.
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
- Delete: happy path
- Resolve: returns correct `Method` constant + expiry semantics
- Healthy: OK + 5xx error path

The `backends/gcs_test.go` and `backends/rustfs_test.go` files are
the canonical templates. Copy the structure; change the fake.

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
            SupportsDelete:        true,
            SupportsExpiry:        true,                       // false for in-memory only
            ExpectedResolveMethod: storage.MethodSignedURL,    // or MethodDirect for in-memory
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
| `SupportsDelete` | Conformance runs full delete-then-not-exists lifecycle | Conformance asserts `Delete` returns `storage.ErrNotSupported` (no current backend uses this branch) |
| `SupportsExpiry` | Conformance asserts `Resolve` returns a non-nil `Expiry` and different CIDs produce different URLs | Conformance asserts `Resolve` returns nil `Expiry` |
| `ExpectedResolveMethod` | Required. Conformance asserts `cred.Method` equals exactly this constant. |

Don't guess — read your `Resolve` implementation and set the field
accordingly. If you ever change the `Method` you return, the conformance
test catches it.

### CID wire-form discipline (ADR-005 §2 — load-bearing)

SDK v7.75 makes `storage.RegisterAlgorithm` part of the public CID
contract and pins `artifactCID.Bytes()` (algorithm_byte || digest), not
`artifactCID.Digest` alone, as the input to the PRE Grant SplitID
derivation (`crypto/artifact/split_id.go:42-53`). A backend that
silently strips the algorithm tag — for example, by keying storage on
`cid.Digest` while ignoring `cid.Algorithm` — will cause recipients to
compute the wrong SplitID for a non-SHA-256 artifact. The on-log
commitment lookup returns the wrong commitment; decryption fails (or
worse, succeeds against a different artifact whose digest happens to
collide under another algorithm).

**The rule for object-store backends:** key your storage on
`cid.String()` (canonical `"<algoname>:<hex>"` form) or `cid.Bytes()`.
Both encode the algorithm byte; both round-trip cleanly through
`storage.ParseCID`. Every supported backend (memory, GCS, RustFS,
MirroredStore) follows this rule.

The conformance suite's `CIDWireForm` scenario asserts the property
end-to-end with two sub-tests — SHA-256 round-trip and a synthetic
16-byte truncated-SHA-256 algorithm registered via
`storage.RegisterAlgorithm`. Both run uniformly on every backend; no
capability gate is required.

---

## 6. Update `TESTING.md`'s layer map

Add a row under Layer 2 (Wave 1):

```
| `backends/<name>.go` | `backends/<name>_test.go` | Your custom fake |
```

---

## 7. Update Wave 2/3 if the backend is cloud-hosted

Wave 2 (testcontainers) only makes sense if there's a reference
implementation you can run in a container. The currently containerized
references are RustFS (S3 wire protocol) and fsouza/fake-gcs-server
(GCS). If your backend has a containerizable reference, add a
`tests/integration/suite_<name>_test.go` consumer plus the matching
container helper in `tests/integration/containers/`.

Wave 3 (real cloud) applies to every cloud-hosted backend. Add
`tests/staging/suite_<name>_test.go` with a dedicated bucket/account
and a per-run unique prefix with `t.Cleanup`. Add the credential set
to `tests/staging/credentials.go`'s `stagingVendors()` so missing
credentials fail loudly at TestMain.

---

## Checklist

Before opening a PR that adds a backend:

- [ ] `backends/<name>.go` — source file
- [ ] `backends/<name>_test.go` — unit tests via httptest server
- [ ] `cmd/artifact-store/main.go` — case added to `createBackend`
- [ ] `config/config.go` — case added to `validate` (primary + mirror set)
- [ ] `config/config_test.go` — case added to `TestLoad_BackendValidation`
      (and the mirror equivalent)
- [ ] `tests/conformance/suite_<name>_test.go` — conformance consumer
- [ ] `internal/testutil/httpfakes.go` — if you added a new fake
      (with full-coverage `httpfakes_test.go` additions to match)
- [ ] `docs/TESTING.md` — layer map row added
- [ ] Wave 2 container helper, if applicable
- [ ] Wave 3 staging suite + `credentials.go` entry, if cloud-hosted
- [ ] `make audit-v775-consumer` passes locally

The conformance suite does most of the work. You bring the wire format;
it brings the contract.
