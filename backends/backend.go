/*
Package backends provides content-addressed object-store implementations.

# What "object store" means here

The artifact store supports exactly one kind of backend: a remote
object store with put/get/head/delete semantics, signed-URL retrieval,
and arbitrary opaque bytes per object. Concretely: GCS, RustFS (the
S3-protocol implementation), and any future provider that satisfies
the BackendProvider interface defined below. Anything that doesn't
fit the shape — pinning networks, append-only logs, key-value stores
with attached metadata, RDBMS-backed blob stores — is out of scope.

The artifact store never computes CIDs, never encrypts, never holds
keys, and never speaks anything but bytes-by-CID. The SDK controls
addressing and encryption; the operator controls the log; the domain
controls policy. This package is the byte plane and nothing more.

# BackendProvider — the extension point

BackendProvider is the contract every object-store backend must satisfy.
The interface is intentionally narrow: 5 SDK-defined ContentStore
methods + Resolve + Healthy. New providers plug in by implementing it
and registering a constructor in cmd/artifact-store/main.go's
createBackend dispatch.

The conformance suite (tests/conformance/) is the test contract every
implementation must pass. It's a single t.Run() call away from any
backend's test file:

    conformance.RunBackendConformance(t, "myprovider", factory, caps)

Adding a new object store is then a four-step recipe:

  1. Implement BackendProvider in backends/<name>.go
  2. Register the kid-keyed dispatch in cmd/artifact-store/main.go
  3. Add a HTTP-mocked test in backends/<name>_test.go using
     internal/testutil.S3Fake / GCSFake or a custom httptest.Server
  4. Wire the conformance run in tests/integration/suite_<name>_test.go

That's the whole extension surface. See docs/ADDING_A_BACKEND.md for
the full walkthrough including the capability flags and test patterns.
*/
package backends

import (
	"time"

	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// BackendProvider is the object-store contract. The supported set
// today is GCS, RustFS, the in-memory reference implementation, and
// any composition via MirroredStore. Third-party object stores that
// satisfy this interface plug in the same way.
//
// The interface composes the SDK's storage.ContentStore (Push, Fetch,
// Pin, Exists, Delete — defined in ortholog-sdk/storage/content_store.go)
// with Resolve (retrieval-credential minting) and Healthy (reachability).
//
// Method semantics:
//
//   - Push must accept any CID whose Algorithm is registered with the
//     SDK's storage.RegisterAlgorithm. Backends that key on cid.String()
//     or cid.Bytes() get this for free; backends that read cid.Digest
//     alone are non-conforming. The CIDWireForm conformance scenario
//     pins the property.
//
//   - Fetch returns storage.ErrContentNotFound for missing CIDs.
//
//   - Pin is best-effort: marks the object for retention. Backends that
//     don't expose a retention primitive may return nil (no-op) — the
//     conformance suite tolerates this.
//
//   - Delete returns nil on success. Object stores all support delete;
//     this is part of the contract.
//
//   - Resolve produces a *storage.RetrievalCredential. Production
//     backends return MethodSignedURL with a non-nil Expiry. The
//     in-memory reference returns MethodDirect with nil expiry.
//
//   - Healthy is the /healthz probe — typically a HEAD on the bucket
//     or a List with limit=1. Returns nil when reachable.
type BackendProvider interface {
	storage.ContentStore

	// Resolve produces a retrieval credential for the given CID.
	// The Method field uses SDK constants: storage.MethodSignedURL
	// for production object-store backends, storage.MethodDirect
	// for the in-memory reference. New retrieval mechanics
	// (CDN-fronted, edge-routed, etc.) compose with the existing
	// vocabulary unless they're genuinely novel; see ADR-005 §5
	// in the SDK.
	Resolve(cid storage.CID, expiry time.Duration) (*storage.RetrievalCredential, error)

	// Healthy checks backend reachability. Used by /healthz.
	Healthy() error
}

// InMemoryBackend is the reference BackendProvider for tests.
// Delegates ContentStore to the SDK's InMemoryContentStore.
// Returns storage.MethodDirect for Resolve. Not for production —
// the data does not survive process restart.
type InMemoryBackend struct {
	store    *storage.InMemoryContentStore
	provider *storage.InMemoryRetrievalProvider
}

// NewInMemoryBackend creates a test backend.
func NewInMemoryBackend() *InMemoryBackend {
	return &InMemoryBackend{
		store:    storage.NewInMemoryContentStore(),
		provider: storage.NewInMemoryRetrievalProvider(),
	}
}

func (b *InMemoryBackend) Push(cid storage.CID, data []byte) error {
	return b.store.Push(cid, data)
}

func (b *InMemoryBackend) Fetch(cid storage.CID) ([]byte, error) {
	return b.store.Fetch(cid)
}

func (b *InMemoryBackend) Pin(cid storage.CID) error {
	return b.store.Pin(cid)
}

func (b *InMemoryBackend) Exists(cid storage.CID) (bool, error) {
	return b.store.Exists(cid)
}

func (b *InMemoryBackend) Delete(cid storage.CID) error {
	return b.store.Delete(cid)
}

func (b *InMemoryBackend) Resolve(cid storage.CID, expiry time.Duration) (*storage.RetrievalCredential, error) {
	return b.provider.Resolve(cid, expiry)
}

func (b *InMemoryBackend) Healthy() error {
	return nil
}
