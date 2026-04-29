package conformance

import (
	"bytes"
	"errors"
	"testing"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// Direct unit cover for runCIDUnregistered. The full scenario runs
// via RunBackendConformance against InMemoryBackend in
// suite_inmemory_test.go; this file pins each invariant individually
// so a regression surfaces with a precise test name.

func TestRunCIDUnregistered_PassesAgainstInMemory(t *testing.T) {
	factory := func() backends.BackendProvider { return backends.NewInMemoryBackend() }
	runCIDUnregistered(t, factory, Capabilities{
		SupportsDelete:        true,
		SupportsExpiry:        false,
		ExpectedResolveMethod: storage.MethodDirect,
	})
}

func TestCIDUnregistered_StringHexFallback(t *testing.T) {
	cid := storage.CID{
		Algorithm: unregisteredAlgoTag,
		Digest:    []byte("16-byte-fakedigt"),
	}
	got := cid.String()
	if got[:5] != "0xfe:" {
		t.Fatalf("CID.String() = %q, want prefix 0xfe:", got)
	}
}

func TestCIDUnregistered_BytesCarriesTag(t *testing.T) {
	cid := storage.CID{
		Algorithm: unregisteredAlgoTag,
		Digest:    []byte("16-byte-fakedigt"),
	}
	got := cid.Bytes()
	if got[0] != byte(unregisteredAlgoTag) {
		t.Fatalf("CID.Bytes()[0] = 0x%02x, want 0x%02x", got[0], byte(unregisteredAlgoTag))
	}
}

func TestCIDUnregistered_VerifyReturnsFalse(t *testing.T) {
	cid := storage.CID{
		Algorithm: unregisteredAlgoTag,
		Digest:    []byte("16-byte-fakedigt"),
	}
	if cid.Verify([]byte("any-bytes")) {
		t.Fatal("Verify returned true under unregistered algorithm")
	}
}

func TestCIDUnregistered_ParseCIDRejectsHexFallback(t *testing.T) {
	hex := "0xfe:31362d627974652d66616b6564696774"
	if _, err := storage.ParseCID(hex); err == nil {
		t.Fatal("ParseCID accepted unregistered hex-fallback name")
	}
}

func TestCIDUnregistered_ParseCIDBytesRejectsTag(t *testing.T) {
	buf := append([]byte{byte(unregisteredAlgoTag)}, []byte("16-byte-fakedigt")...)
	if _, err := storage.ParseCIDBytes(buf); err == nil {
		t.Fatal("ParseCIDBytes accepted unregistered tag")
	}
}

func TestCIDUnregistered_ComputeWithPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("ComputeWith did not panic on unregistered algorithm")
		}
	}()
	_ = storage.ComputeWith([]byte("any"), unregisteredAlgoTag)
}

func TestCIDUnregistered_BackendAcceptsUnregisteredPush(t *testing.T) {
	b := backends.NewInMemoryBackend()
	cid := storage.CID{
		Algorithm: unregisteredAlgoTag,
		Digest:    []byte("16-byte-fakedigt"),
	}
	data := []byte("unit-unregistered-push")
	if err := b.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}
	got, err := b.Fetch(cid)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("bytes drift on round-trip under unregistered tag")
	}
}

func TestCIDUnregistered_BackendFetchMissingReturnsNotFound(t *testing.T) {
	b := backends.NewInMemoryBackend()
	cid := storage.CID{
		Algorithm: unregisteredAlgoTag,
		Digest:    []byte("never-pushed-16b"),
	}
	_, err := b.Fetch(cid)
	if !errors.Is(err, storage.ErrContentNotFound) {
		t.Fatalf("err=%v, want ErrContentNotFound", err)
	}
}
