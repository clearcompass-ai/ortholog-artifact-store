/*
Package backends provides content-addressed blob storage implementations.

Each backend implements BackendProvider: the union of the SDK's ContentStore
(5 methods: Push, Fetch, Pin, Exists, Delete) and Resolve (RetrievalProvider).

The artifact store never computes CIDs, never encrypts, never holds keys.
It stores bytes and gives them back. The SDK controls addressing and encryption.
*/
package backends

import (
	"time"

	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// BackendProvider is the complete backend interface.
// Every backend (GCS, S3, IPFS, in-memory) implements all methods.
type BackendProvider interface {
	storage.ContentStore

	// Resolve produces a retrieval credential for the given CID.
	// The Method field uses SDK constants: storage.MethodSignedURL,
	// storage.MethodIPFS, or storage.MethodDirect.
	Resolve(cid storage.CID, expiry time.Duration) (*storage.RetrievalCredential, error)

	// Healthy checks backend reachability. Used by /healthz.
	Healthy() error
}

// InMemoryBackend is a reference BackendProvider for testing.
// Delegates ContentStore to the SDK's InMemoryContentStore.
// Returns storage.MethodDirect for Resolve.
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
