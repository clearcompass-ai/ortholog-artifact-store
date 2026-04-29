package conformance

import (
	"bytes"
	"errors"
	"testing"

	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// ─── Algorithm-agnostic backend contract ──────────────────────────────
//
// The SDK validates algorithm registration at the boundary —
// ParseCID rejects an unknown name (registry miss); ParseCIDBytes
// rejects an unknown tag. Backends, however, key on cid.String() or
// cid.Bytes() and inherit deterministic well-formedness from the
// CID struct itself. They MUST accept any CID whose String() and
// Bytes() are well-formed, regardless of whether the algorithm tag
// has been registered with storage.RegisterAlgorithm in the current
// process.
//
// This is load-bearing for cross-process workflows. An artifact
// uploaded under sha-256 in one process and fetched in another
// process that loaded a different set of registered algorithms must
// still Fetch successfully — the backend is the bytes plane and
// nothing more. Re-validating algorithm registration in the backend
// would silently break artifacts pushed before a binary upgrade
// dropped a registration.
//
// SDK exports for unregistered tags:
//   cid.String() → "0x<hex-tag>:<digest-hex>"  (deterministic fallback)
//   cid.Bytes()  → tag-byte || digest          (no validation)
//
// The scenarios below confirm:
//   - Push accepts a hand-crafted CID with unregistered tag 0xFE
//   - Fetch returns the same bytes under that CID
//   - The fallback string form parses-back-to-bytes-form symmetrically
//     for the backend (storage key stability across re-construction)

// unregisteredAlgoTag is deliberately outside any RegisterAlgorithm
// call across the test binary. We chose 0xFE to avoid collisions with:
//   - 0x12 (AlgoSHA256)
//   - 0xC1, 0xC2 (api package + scenarios_cid_wire.go)
//   - 0xE2, 0xF1 (SDK's own cid_test.go fixtures)
const unregisteredAlgoTag storage.HashAlgorithm = 0xFE

func runCIDUnregistered(t *testing.T, factory Factory, _ Capabilities) {
	t.Run("push_accepts_cid_with_unregistered_algorithm_tag", func(t *testing.T) {
		// Hand-craft a CID under an unregistered tag. The digest can
		// be any well-formed bytes — the backend doesn't care about
		// hash provenance, only about deterministic key stability.
		b := factory()
		data := []byte("unregistered/push")
		cid := storage.CID{
			Algorithm: unregisteredAlgoTag,
			Digest:    []byte("16-byte-fakedigt"),
		}
		if err := b.Push(cid, data); err != nil {
			t.Fatalf("Push under unregistered algorithm tag 0x%02x: %v "+
				"(backends MUST be algorithm-agnostic at the bytes plane)",
				byte(unregisteredAlgoTag), err)
		}
		got, err := b.Fetch(cid)
		if err != nil {
			t.Fatalf("Fetch under unregistered tag: %v", err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("Fetch under unregistered tag: bytes drift")
		}
	})

	t.Run("string_form_uses_hex_fallback_for_unregistered", func(t *testing.T) {
		// SDK contract: cid.String() falls back to 0x<hex-tag> when
		// the tag is unregistered. Pin this — it's the storage key
		// every backend uses and changing it strands data.
		cid := storage.CID{
			Algorithm: unregisteredAlgoTag,
			Digest:    []byte("16-byte-fakedigt"),
		}
		got := cid.String()
		const wantPrefix = "0xfe:"
		if len(got) < len(wantPrefix) || got[:len(wantPrefix)] != wantPrefix {
			t.Fatalf("CID.String() = %q, want prefix %q", got, wantPrefix)
		}
	})

	t.Run("bytes_form_carries_tag_for_unregistered", func(t *testing.T) {
		// Bytes() must lead with the tag byte even when the tag is
		// unregistered. ADR-005 §2 SplitID derivation hashes
		// cid.Bytes(); a backend that strips the tag silently
		// produces SplitID drift across registrations.
		cid := storage.CID{
			Algorithm: unregisteredAlgoTag,
			Digest:    []byte("16-byte-fakedigt"),
		}
		got := cid.Bytes()
		if len(got) == 0 {
			t.Fatal("CID.Bytes() returned empty for unregistered tag")
		}
		if got[0] != byte(unregisteredAlgoTag) {
			t.Fatalf("CID.Bytes()[0] = 0x%02x, want 0x%02x",
				got[0], byte(unregisteredAlgoTag))
		}
	})

	t.Run("parse_string_rejects_unregistered_at_boundary", func(t *testing.T) {
		// Symmetric with the algorithm-agnostic Push: ParseCID at the
		// HTTP boundary REJECTS unregistered names because it
		// length-checks against algorithmDigestSize. This pins the
		// "boundary validates, backend tolerates" contract.
		hex := "0xfe:31362d627974652d66616b6564696774"
		_, err := storage.ParseCID(hex)
		if err == nil {
			t.Fatal("ParseCID accepted unregistered tag at boundary; want error")
		}
		// A reasonable error mention helps audit logs; we don't pin
		// the exact message but assert at least non-empty.
		if err.Error() == "" {
			t.Fatal("ParseCID error message empty")
		}
	})

	t.Run("parse_bytes_rejects_unregistered_at_boundary", func(t *testing.T) {
		// Symmetric on the bytes-form boundary.
		buf := append([]byte{byte(unregisteredAlgoTag)}, []byte("16-byte-fakedigt")...)
		_, err := storage.ParseCIDBytes(buf)
		if err == nil {
			t.Fatal("ParseCIDBytes accepted unregistered tag at boundary; want error")
		}
	})

	t.Run("verify_returns_false_for_unregistered_algorithm", func(t *testing.T) {
		// cid.Verify must return false when the algorithm has no
		// registered hash function — there is no way to recompute
		// the digest. Pin this so a regression where Verify silently
		// returns true on unregistered tags is caught.
		cid := storage.CID{
			Algorithm: unregisteredAlgoTag,
			Digest:    []byte("16-byte-fakedigt"),
		}
		if cid.Verify([]byte("any-bytes")) {
			t.Fatal("cid.Verify returned true under unregistered algorithm; want false")
		}
	})

	t.Run("compute_panics_on_unregistered_algorithm", func(t *testing.T) {
		// SDK contract: ComputeWith panics when the algorithm is not
		// registered. This is documented behavior — "fail loudly when
		// the test binary forgot to register". Pin it so a future
		// SDK change to silent-zero-digest gets caught.
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("ComputeWith did not panic on unregistered algorithm; want panic")
			}
		}()
		_ = storage.ComputeWith([]byte("any"), unregisteredAlgoTag)
	})

	t.Run("backend_remains_healthy_after_unregistered_push", func(t *testing.T) {
		// A push under an unregistered tag must not corrupt the
		// backend's overall state. After the push, Healthy must
		// still return nil and unrelated SHA-256 push/fetch must
		// continue to work.
		b := factory()
		odd := storage.CID{Algorithm: unregisteredAlgoTag, Digest: []byte("16-byte-fakedigt")}
		if err := b.Push(odd, []byte("noise")); err != nil {
			t.Fatalf("Push of odd CID failed unexpectedly: %v", err)
		}
		if err := b.Healthy(); err != nil {
			t.Fatalf("Healthy after odd push: %v", err)
		}

		data := []byte("after-odd-push/sha256")
		cid := storage.Compute(data)
		if err := b.Push(cid, data); err != nil {
			t.Fatalf("SHA-256 Push after odd push: %v", err)
		}
		got, err := b.Fetch(cid)
		if err != nil {
			t.Fatalf("SHA-256 Fetch after odd push: %v", err)
		}
		if !bytes.Equal(got, data) {
			t.Fatal("SHA-256 fetch returned different bytes after odd push")
		}
	})

	t.Run("fetch_missing_unregistered_cid_returns_not_found", func(t *testing.T) {
		// The not-found classification works under any algorithm
		// (the backend keys on cid.String(), the lookup misses). Pin
		// that the SDK sentinel still propagates.
		b := factory()
		odd := storage.CID{
			Algorithm: unregisteredAlgoTag,
			Digest:    []byte("never-pushed-16b"),
		}
		_, err := b.Fetch(odd)
		if !errors.Is(err, storage.ErrContentNotFound) {
			t.Fatalf("Fetch missing unregistered CID: err=%v, want ErrContentNotFound", err)
		}
	})
}
