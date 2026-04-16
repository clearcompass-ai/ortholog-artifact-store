package backends

import (
	"bytes"
	"testing"

	"github.com/clearcompass-ai/ortholog-artifact-store/internal/testutil"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// TestMain is shared by every test in the backends package. goleak
// catches dangling goroutines — especially important for mirrored.go's
// asyncPinWorker, which is the only goroutine-owning backend.
func TestMain(m *testing.M) {
	testutil.RunWithGoleak(m)
}

// TestInMemoryBackend_Healthy is a trivial sanity check — the InMemoryBackend
// should always report healthy because it has no external dependencies.
func TestInMemoryBackend_Healthy(t *testing.T) {
	if err := NewInMemoryBackend().Healthy(); err != nil {
		t.Fatalf("InMemoryBackend.Healthy: %v", err)
	}
}

// TestInMemoryBackend_ResolveReturnsMethodDirect locks the in-memory
// backend's Resolve contract. Other backends return MethodSignedURL or
// MethodIPFS; the in-memory backend is the only one that returns
// MethodDirect. If this ever changes, downstream consumers that dispatch
// on Method need to be updated.
func TestInMemoryBackend_ResolveReturnsMethodDirect(t *testing.T) {
	b := NewInMemoryBackend()
	data := []byte("method-check")
	cid := storage.Compute(data)
	if err := b.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}

	cred, err := b.Resolve(cid, 0) // expiry is meaningless for direct
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cred.Method != storage.MethodDirect {
		t.Fatalf("Method: want %s, got %s", storage.MethodDirect, cred.Method)
	}
	if cred.Expiry != nil {
		t.Fatalf("MethodDirect credentials must have nil Expiry, got %v", *cred.Expiry)
	}
}

// TestInMemoryBackend_RoundTrip is a belt-and-suspenders test that the
// conformance suite also covers. Kept here so `go test ./backends/`
// alone gives a meaningful smoke signal.
func TestInMemoryBackend_RoundTrip(t *testing.T) {
	b := NewInMemoryBackend()
	data := []byte("round-trip")
	cid := storage.Compute(data)

	if err := b.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}
	got, err := b.Fetch(cid)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("round-trip byte mismatch")
	}
}
