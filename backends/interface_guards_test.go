package backends

import (
	"testing"

	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// Runtime mirror of the compile-time guards in interface_guards.go.
// The compile-time guards are the load-bearing version — these tests
// exist so that:
//   1. Tooling that doesn't run the type checker (some IDE plugins,
//      `go vet` on partial builds) still surfaces a contract break.
//   2. A future refactor that swaps the guards for runtime checks
//      can lean on an existing test rather than rewriting the layer.
//
// Each test does a single typed assignment + nil-check. The cost is
// ~zero; the value is a per-backend test name in the failure report.

func TestInterfaceGuard_InMemoryBackend(t *testing.T) {
	var (
		_ storage.ContentStore = (*InMemoryBackend)(nil)
		_ BackendProvider      = (*InMemoryBackend)(nil)
	)
	b := NewInMemoryBackend()
	if b == nil {
		t.Fatal("NewInMemoryBackend returned nil")
	}
	// Use the backend through the SDK-level interface so a regression
	// in the embedded composition (e.g. someone replaces the embed
	// with a non-conforming wrapper) fails here.
	var cs storage.ContentStore = b
	cid := storage.Compute([]byte("interface-guard/inmem"))
	if err := cs.Push(cid, []byte("interface-guard/inmem")); err != nil {
		t.Fatalf("Push through ContentStore interface: %v", err)
	}
}

func TestInterfaceGuard_GCSBackend(t *testing.T) {
	var (
		_ storage.ContentStore = (*GCSBackend)(nil)
		_ BackendProvider      = (*GCSBackend)(nil)
	)
}

func TestInterfaceGuard_RustFSBackend(t *testing.T) {
	var (
		_ storage.ContentStore = (*RustFSBackend)(nil)
		_ BackendProvider      = (*RustFSBackend)(nil)
	)
}

func TestInterfaceGuard_MirroredStore(t *testing.T) {
	var (
		_ storage.ContentStore = (*MirroredStore)(nil)
		_ BackendProvider      = (*MirroredStore)(nil)
	)
	primary := NewInMemoryBackend()
	mirror := NewInMemoryBackend()
	ms := NewMirroredStore(primary, mirror, MirroredConfig{Mode: MirrorModeSync})
	t.Cleanup(ms.Close)

	var bp BackendProvider = ms
	cid := storage.Compute([]byte("interface-guard/mirrored"))
	if err := bp.Push(cid, []byte("interface-guard/mirrored")); err != nil {
		t.Fatalf("Push through BackendProvider interface: %v", err)
	}
	if err := bp.Healthy(); err != nil {
		t.Fatalf("Healthy through BackendProvider interface: %v", err)
	}
}
