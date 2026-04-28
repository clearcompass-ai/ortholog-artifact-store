package backends

import (
	"bytes"
	"testing"

	"github.com/clearcompass-ai/ortholog-artifact-store/internal/testutil"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// TestMain is shared by every test in the backends package. goleak
// catches any dangling goroutine in any backend implementation. None
// of the supported object-store backends own background goroutines
// today; goleak stays plumbed in as a forward guard.
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

// TestInMemoryBackend_Exists pins the Exists contract in both
// directions — true after Push, false before Push.
func TestInMemoryBackend_Exists(t *testing.T) {
	b := NewInMemoryBackend()
	data := []byte("exists-check")
	cid := storage.Compute(data)

	if exists, err := b.Exists(cid); err != nil || exists {
		t.Fatalf("Exists before Push: want (false, nil), got (%v, %v)", exists, err)
	}
	if err := b.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if exists, err := b.Exists(cid); err != nil || !exists {
		t.Fatalf("Exists after Push: want (true, nil), got (%v, %v)", exists, err)
	}
}

// TestInMemoryBackend_Pin pins the Pin contract: nil on a CID that
// exists, ErrContentNotFound on a CID that doesn't. The SDK's
// InMemoryContentStore is what enforces this; the wrapper just
// delegates.
func TestInMemoryBackend_Pin(t *testing.T) {
	b := NewInMemoryBackend()
	data := []byte("pin-check")
	cid := storage.Compute(data)

	if err := b.Pin(cid); err == nil {
		t.Fatal("Pin on missing CID: want error, got nil")
	}

	if err := b.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if err := b.Pin(cid); err != nil {
		t.Fatalf("Pin on existing CID: %v", err)
	}
}

// TestInMemoryBackend_Delete pins the Delete contract: bytes are
// gone after Delete, and a subsequent Fetch returns
// ErrContentNotFound. Delete on a missing CID is tolerant (no error)
// per the SDK reference impl.
func TestInMemoryBackend_Delete(t *testing.T) {
	b := NewInMemoryBackend()
	data := []byte("delete-check")
	cid := storage.Compute(data)

	if err := b.Delete(cid); err != nil {
		t.Fatalf("Delete on missing CID should be tolerant: %v", err)
	}

	if err := b.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if err := b.Delete(cid); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if exists, _ := b.Exists(cid); exists {
		t.Fatal("Exists after Delete: want false, got true")
	}
}
