package conformance

import (
	"testing"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// Direct unit cover for runCIDVerify. The full suite exercise lives
// in suite_inmemory_test.go via RunBackendConformance — this gives
// per-helper failure names so a regression surfaces immediately.

func TestRunCIDVerify_PassesAgainstInMemory(t *testing.T) {
	factory := func() backends.BackendProvider { return backends.NewInMemoryBackend() }
	runCIDVerify(t, factory, Capabilities{
		SupportsDelete:        true,
		SupportsExpiry:        false,
		ExpectedResolveMethod: storage.MethodDirect,
	})
}

// TestCIDVerify_AcceptsCorrectBytes is a pure-SDK assertion — the
// scenario embeds it inside a backend round-trip, but the predicate
// is CPU-only. Pin it in isolation so an SDK regression on Verify's
// happy path fails this test specifically.
func TestCIDVerify_AcceptsCorrectBytes(t *testing.T) {
	data := []byte("unit-verify-correct")
	cid := storage.Compute(data)
	if !cid.Verify(data) {
		t.Fatal("cid.Verify(original) = false; want true")
	}
}

func TestCIDVerify_RejectsWrongBytes(t *testing.T) {
	cid := storage.Compute([]byte("unit-verify-original"))
	if cid.Verify([]byte("unit-verify-different")) {
		t.Fatal("cid.Verify(different) = true; want false")
	}
}

func TestCIDVerify_RejectsTruncatedBytes(t *testing.T) {
	original := []byte("unit-verify-truncation-target")
	cid := storage.Compute(original)
	if cid.Verify(original[:len(original)-1]) {
		t.Fatal("cid.Verify(truncated) = true; want false")
	}
}

func TestCIDEqual_Reflexive(t *testing.T) {
	cid := storage.Compute([]byte("unit-equal-self"))
	if !cid.Equal(cid) {
		t.Fatal("cid.Equal(cid) = false")
	}
}

func TestCIDEqual_DifferentDigestsAreNotEqual(t *testing.T) {
	a := storage.Compute([]byte("a"))
	b := storage.Compute([]byte("b"))
	if a.Equal(b) {
		t.Fatal("different-digest CIDs reported equal")
	}
}

func TestCIDEqual_DifferentAlgorithmsAreNotEqual(t *testing.T) {
	registerWireFormTestAlgorithm()
	data := []byte("unit-equal-algorithms")
	sha := storage.ComputeWith(data, storage.AlgoSHA256)
	custom := storage.ComputeWith(data, wireFormTestAlgoTag)
	if sha.Equal(custom) {
		t.Fatal("different-algorithm CIDs reported equal")
	}
}

func TestCIDIsZero_DefaultStruct(t *testing.T) {
	var zero storage.CID
	if !zero.IsZero() {
		t.Fatal("zero-value CID.IsZero() = false")
	}
}

func TestCIDIsZero_ComputedIsNotZero(t *testing.T) {
	cid := storage.Compute([]byte("unit-iszero-computed"))
	if cid.IsZero() {
		t.Fatal("computed CID.IsZero() = true")
	}
}
