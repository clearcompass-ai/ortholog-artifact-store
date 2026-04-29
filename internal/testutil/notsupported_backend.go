package testutil

import (
	"sync"
	"time"

	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// ─── NotSupportedBackend — exercises the SupportsDelete=false branch ──
//
// Every supported backend in production (GCS, RustFS, MirroredStore,
// InMemoryBackend) declares SupportsDelete=true. That makes the
// conformance suite's SupportsDelete=false branch — which expects
// Delete to return storage.ErrNotSupported — dead code in the test
// matrix until a future append-only backend lands.
//
// NotSupportedBackend is a tiny synthetic BackendProvider that returns
// storage.ErrNotSupported from Delete. Wired into the conformance suite
// with caps.SupportsDelete=false, it keeps the SDK's ErrNotSupported
// sentinel under continuous test coverage. A regression where a backend
// silently returns nil from a missing-feature path (or returns a
// generic error instead of the SDK sentinel) is caught here.
//
// The backend is exported from internal/testutil/ rather than backends/
// because it is a test fixture, not a production implementation. It
// embeds storage.InMemoryContentStore for the supported operations
// (Push/Fetch/Pin/Exists) so the lifecycle scenarios that don't hit
// Delete still pass cleanly.

// NotSupportedBackend is a BackendProvider whose Delete returns
// storage.ErrNotSupported. Used to exercise the SupportsDelete=false
// branch of conformance scenarios.
//
// Implements:
//   - storage.ContentStore via embedded InMemoryContentStore (except
//     Delete, which is overridden)
//   - Resolve via storage.MethodDirect (matches InMemoryRetrievalProvider)
//   - Healthy via a static nil
type NotSupportedBackend struct {
	mu       sync.Mutex
	store    *storage.InMemoryContentStore
	provider *storage.InMemoryRetrievalProvider
}

// NewNotSupportedBackend returns a fresh, empty backend whose Delete
// returns storage.ErrNotSupported. Push/Fetch/Pin/Exists behave like
// InMemoryContentStore.
func NewNotSupportedBackend() *NotSupportedBackend {
	return &NotSupportedBackend{
		store:    storage.NewInMemoryContentStore(),
		provider: storage.NewInMemoryRetrievalProvider(),
	}
}

// Push delegates to the embedded InMemoryContentStore.
func (b *NotSupportedBackend) Push(cid storage.CID, data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.store.Push(cid, data)
}

// Fetch delegates to the embedded InMemoryContentStore.
func (b *NotSupportedBackend) Fetch(cid storage.CID) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.store.Fetch(cid)
}

// Pin delegates to the embedded InMemoryContentStore.
func (b *NotSupportedBackend) Pin(cid storage.CID) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.store.Pin(cid)
}

// Exists delegates to the embedded InMemoryContentStore.
func (b *NotSupportedBackend) Exists(cid storage.CID) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.store.Exists(cid)
}

// Delete is the load-bearing override: returns storage.ErrNotSupported
// unconditionally. The conformance suite asserts errors.Is matches.
func (b *NotSupportedBackend) Delete(_ storage.CID) error {
	return storage.ErrNotSupported
}

// Resolve returns a MethodDirect credential matching the
// InMemoryRetrievalProvider behavior — keeps the conformance Resolve
// scenarios on the no-expiry path.
func (b *NotSupportedBackend) Resolve(cid storage.CID, expiry time.Duration) (*storage.RetrievalCredential, error) {
	return b.provider.Resolve(cid, expiry)
}

// Healthy is always nil for this in-process backend.
func (b *NotSupportedBackend) Healthy() error { return nil }
