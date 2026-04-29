package conformance

import (
	"testing"

	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// ─── CID.Verify / Equal / IsZero contract ─────────────────────────────
//
// api/push.go's PushHandler relies on three CID predicates from the SDK
// every request:
//
//   - cid.Verify(data)  — the verify-mode digest check (push.go:117)
//   - cid.Equal(other)  — implied by ParseCID round-trip (covered in
//                          scenarios_cid_parse.go) and by audit log
//                          dedup logic
//   - cid.IsZero()      — boundary check for "this CID came from a
//                          zero-valued struct, not a real header"
//
// All three are CPU-only — no backend interaction — but the conformance
// suite still runs them per-backend so that a future backend that
// returns a CID-typed value (e.g. via Resolve.Method-extension) is
// caught if its construction violates the predicates.

func runCIDVerify(t *testing.T, factory Factory, _ Capabilities) {
	t.Run("verify_accepts_correct_bytes", func(t *testing.T) {
		// Push the bytes, fetch them back, and assert the CID
		// minted by the SDK still verifies the round-tripped bytes.
		// Catches a backend that silently re-encodes (e.g. trailing
		// whitespace) — Verify would fail though Fetch returned data.
		b := factory()
		data := []byte("verify/correct-bytes")
		cid := storage.Compute(data)
		if err := b.Push(cid, data); err != nil {
			t.Fatalf("Push: %v", err)
		}
		got, err := b.Fetch(cid)
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if !cid.Verify(got) {
			t.Fatal("cid.Verify(fetched) = false; backend mutated bytes between Push and Fetch")
		}
	})

	t.Run("verify_rejects_wrong_bytes", func(t *testing.T) {
		// Verify(other) must be false for any non-matching payload.
		// The SDK's constant-time XOR loop is fast, but the test
		// pins the boolean-result contract — not the timing.
		cid := storage.Compute([]byte("verify/original"))
		if cid.Verify([]byte("verify/different")) {
			t.Fatal("cid.Verify(other) = true; want false")
		}
	})

	t.Run("verify_rejects_truncated_bytes", func(t *testing.T) {
		// One-byte truncation is the most common silent corruption
		// mode in HTTP plumbing (off-by-one on Content-Length, an
		// over-eager io.LimitReader). Pin Verify's failure here.
		original := []byte("verify/truncation-target-bytes")
		cid := storage.Compute(original)
		if cid.Verify(original[:len(original)-1]) {
			t.Fatal("cid.Verify(truncated) = true; want false")
		}
	})

	t.Run("verify_uses_algorithm_to_rehash", func(t *testing.T) {
		// A hand-crafted CID with a digest that does NOT match the
		// algorithm-specific hash of the data must fail Verify. This
		// catches a regression where Verify compares against a stored
		// digest without re-hashing — silently letting any byte
		// payload pass for a CID with arbitrary digest bytes.
		registerWireFormTestAlgorithm()
		data := []byte("verify/algo-rehash")
		imposter := storage.CID{
			Algorithm: wireFormTestAlgoTag,
			Digest:    make([]byte, wireFormTestDigestSize), // all-zero digest
		}
		if imposter.Verify(data) {
			t.Fatal("CID with all-zero digest Verify()-passed real bytes")
		}
	})

	t.Run("equal_self", func(t *testing.T) {
		cid := storage.Compute([]byte("equal/self"))
		if !cid.Equal(cid) {
			t.Fatal("cid.Equal(cid) = false; reflexivity violated")
		}
	})

	t.Run("equal_returns_false_for_different_digest", func(t *testing.T) {
		a := storage.Compute([]byte("equal/a"))
		b := storage.Compute([]byte("equal/b"))
		if a.Equal(b) {
			t.Fatal("cid.Equal returned true for different-digest CIDs")
		}
	})

	t.Run("equal_returns_false_for_different_algorithm", func(t *testing.T) {
		registerWireFormTestAlgorithm()
		data := []byte("equal/algo")
		sha := storage.ComputeWith(data, storage.AlgoSHA256)
		custom := storage.ComputeWith(data, wireFormTestAlgoTag)
		if sha.Equal(custom) {
			t.Fatal("cid.Equal returned true across different algorithms")
		}
	})

	t.Run("iszero_on_default_struct", func(t *testing.T) {
		var zero storage.CID
		if !zero.IsZero() {
			t.Fatal("default-constructed CID.IsZero() = false; want true")
		}
	})

	t.Run("iszero_false_on_computed", func(t *testing.T) {
		cid := storage.Compute([]byte("iszero/computed"))
		if cid.IsZero() {
			t.Fatal("computed CID.IsZero() = true; want false")
		}
	})

	t.Run("iszero_false_on_custom_algorithm", func(t *testing.T) {
		// Even a CID whose digest is all-zero bytes shouldn't be
		// IsZero unless its Digest slice is empty. Pin this to
		// catch a regression where IsZero looks at byte values
		// instead of slice length.
		registerWireFormTestAlgorithm()
		zeroDigest := storage.CID{
			Algorithm: wireFormTestAlgoTag,
			Digest:    make([]byte, wireFormTestDigestSize),
		}
		if zeroDigest.IsZero() {
			t.Fatal("CID with non-empty zero-byte digest reports IsZero(); want false")
		}
	})
}
