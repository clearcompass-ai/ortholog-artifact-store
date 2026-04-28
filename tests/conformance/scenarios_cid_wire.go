package conformance

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"

	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// ─── v7.75 CID wire-form discipline (ADR-005 §2) ──────────────────────
//
// SDK v7.75 makes storage.RegisterAlgorithm part of the public CID
// contract and mandates that the PRE Grant SplitID derivation hashes
// artifactCID.Bytes() (algorithm_byte || digest), not artifactCID.Digest
// alone (crypto/artifact/split_id.go:42-53). Any backend that round-trips
// a CID such that the algorithm byte is silently dropped will cause
// recipients to compute the wrong SplitID for an artifact stored under
// a non-default algorithm — the on-log commitment lookup returns the
// wrong commitment, and decryption fails (or worse, succeeds against a
// different artifact with the same digest under a different algorithm).
//
// This scenario pins the property at the conformance layer:
//
//   - SHA-256 (default) round-trips through every backend with
//     CID.Bytes() preserved (algorithm byte + digest), via the canonical
//     storage key cid.String().
//   - A CID minted under a non-default algorithm registered through
//     storage.RegisterAlgorithm either round-trips with byte-identical
//     CID.Bytes() (general-purpose backends — memory, GCS, RustFS), or
//     is rejected at the call boundary with storage.ErrNotSupported
//     (algorithm-pinned backends — IPFS, gated by Capabilities.SHA256Only).
//
// Silent corruption — accepting a non-SHA-256 push and storing it under
// a SHA-256 CID, for example — is forbidden, and this scenario fails
// loudly when it occurs.

// wireFormTestAlgoTag is the multicodec-style 1-byte tag used by the
// cross-algorithm wire-form scenario. 0xC2 is outside the SDK's pinned
// AlgoSHA256 (0x12), outside the SDK's own cid_test.go fixtures
// (0xE2, 0xF1), and outside the artifact-store api package's fixture
// (0xC1) so the registry does not collide across binaries.
const wireFormTestAlgoTag storage.HashAlgorithm = 0xC2

// wireFormTestAlgoName is the canonical string prefix for CIDs minted
// under wireFormTestAlgoTag. Format: "<name>:<hex>".
const wireFormTestAlgoName = "test-algo-conformance"

// wireFormTestDigestSize is deliberately not 32, so any regression
// that re-introduces a hard-coded SHA-256 digest length silently
// fails to round-trip.
const wireFormTestDigestSize = 16

// registerWireFormTestAlgorithmOnce ensures the synthetic algorithm
// registration happens exactly once per test binary. RegisterAlgorithm
// is idempotent (it overwrites map entries) but doing it once keeps
// the registration site obvious in stack traces.
var registerWireFormTestAlgorithmOnce sync.Once

func registerWireFormTestAlgorithm() {
	registerWireFormTestAlgorithmOnce.Do(func() {
		storage.RegisterAlgorithm(
			wireFormTestAlgoTag,
			wireFormTestAlgoName,
			wireFormTestDigestSize,
			func(data []byte) []byte {
				h := sha256.Sum256(data)
				return h[:wireFormTestDigestSize]
			},
		)
	})
}

func runCIDWireForm(t *testing.T, factory Factory, caps Capabilities) {
	t.Run("sha256_round_trip_preserves_bytes_form", func(t *testing.T) {
		// The default-algorithm round-trip is the property every
		// backend must honor. We push under a SHA-256 CID, fetch
		// back, recompute the CID under the same algorithm, and
		// assert byte-identical CID.Bytes() — the wire form ADR-005
		// §2 mandates for the PRE Grant SplitID derivation.
		b := factory()
		data := []byte("conformance/wire-form/sha256-roundtrip")
		cid := storage.Compute(data)

		if cid.Algorithm != storage.AlgoSHA256 {
			t.Fatalf("test setup: storage.Compute should default to SHA-256, got 0x%02x",
				byte(cid.Algorithm))
		}

		if err := b.Push(cid, data); err != nil {
			t.Fatalf("Push: %v", err)
		}
		got, err := b.Fetch(cid)
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("byte mismatch on Fetch: want=%q got=%q", data, got)
		}

		// The strong assertion: the CID we pushed under and the CID
		// derived from the bytes we fetched back have identical
		// Bytes() (algorithm tag + digest). If the backend stripped
		// the algorithm tag on the read side, this fails — even when
		// the bytes themselves match — because storage.Compute(got)
		// produces a CID whose Bytes() leads with the algorithm byte.
		recomputed := storage.ComputeWith(got, cid.Algorithm)
		if !bytes.Equal(cid.Bytes(), recomputed.Bytes()) {
			t.Fatalf("CID.Bytes() drift across round-trip:\n  pushed   : %x\n  recomputed: %x",
				cid.Bytes(), recomputed.Bytes())
		}
		if cid.Bytes()[0] != byte(storage.AlgoSHA256) {
			t.Fatalf("CID.Bytes()[0] = 0x%02x, want 0x%02x (AlgoSHA256)",
				cid.Bytes()[0], byte(storage.AlgoSHA256))
		}
	})

	if caps.SHA256Only {
		t.Run("non_sha256_algorithm_rejected_with_err_not_supported", func(t *testing.T) {
			// Backends that pin SHA-256 (currently IPFS, via
			// multihash 0x12) must fail closed when handed a CID
			// minted under any other algorithm. Silent acceptance
			// would be the exact wire-form regression ADR-005 §2
			// is designed to prevent — the algorithm byte would be
			// stripped and the SDK-side SplitID derivation would
			// look up the wrong commitment.
			registerWireFormTestAlgorithm()
			b := factory()
			data := []byte("conformance/wire-form/non-sha256")
			cid := storage.ComputeWith(data, wireFormTestAlgoTag)

			err := b.Push(cid, data)
			if !errors.Is(err, storage.ErrNotSupported) {
				t.Fatalf("Push under %s: want errors.Is(err, ErrNotSupported), got %v",
					wireFormTestAlgoName, err)
			}

			// And the same property on the read path: Fetch / Exists
			// must reject before issuing any backend call. If the
			// guard is missing on a read path, an attacker could
			// drive a CID-mismatch path against the backend by
			// forging a non-SHA-256 CID for content that happens to
			// share the digest tail under SHA-256. Fail-closed here
			// closes that class.
			if _, err := b.Fetch(cid); !errors.Is(err, storage.ErrNotSupported) {
				t.Fatalf("Fetch under %s: want ErrNotSupported, got %v",
					wireFormTestAlgoName, err)
			}
			if _, err := b.Exists(cid); !errors.Is(err, storage.ErrNotSupported) {
				t.Fatalf("Exists under %s: want ErrNotSupported, got %v",
					wireFormTestAlgoName, err)
			}
		})
		return
	}

	t.Run("non_sha256_algorithm_round_trips_with_bytes_preserved", func(t *testing.T) {
		// General-purpose backends (memory, GCS, RustFS) key on
		// cid.String() — the canonical "<algoname>:<hex>" form — so
		// the algorithm byte survives Push→Fetch transparently. The
		// scenario asserts that property end-to-end: push under a
		// non-default algorithm, fetch back, recompute the CID under
		// the same algorithm, and confirm CID.Bytes() is identical.
		// Failure here means the backend either stripped the algorithm
		// tag from its storage key or rejected a non-SHA-256 CID it
		// should have accepted.
		registerWireFormTestAlgorithm()
		b := factory()
		data := []byte("conformance/wire-form/multi-algo-roundtrip")
		cid := storage.ComputeWith(data, wireFormTestAlgoTag)

		if cid.Algorithm != wireFormTestAlgoTag {
			t.Fatalf("test setup: ComputeWith returned algorithm 0x%02x, want 0x%02x",
				byte(cid.Algorithm), byte(wireFormTestAlgoTag))
		}
		if len(cid.Digest) != wireFormTestDigestSize {
			t.Fatalf("test setup: digest size=%d, want %d (proves the test does not assume 32-byte digests)",
				len(cid.Digest), wireFormTestDigestSize)
		}

		if err := b.Push(cid, data); err != nil {
			t.Fatalf("Push under %s: %v (general-purpose backends must accept any registered algorithm)",
				wireFormTestAlgoName, err)
		}
		got, err := b.Fetch(cid)
		if err != nil {
			t.Fatalf("Fetch under %s: %v", wireFormTestAlgoName, err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("byte mismatch on Fetch under %s", wireFormTestAlgoName)
		}

		recomputed := storage.ComputeWith(got, cid.Algorithm)
		if !bytes.Equal(cid.Bytes(), recomputed.Bytes()) {
			t.Fatalf("CID.Bytes() drift across round-trip under %s:\n  pushed    : %x\n  recomputed: %x",
				wireFormTestAlgoName, cid.Bytes(), recomputed.Bytes())
		}
		if cid.Bytes()[0] != byte(wireFormTestAlgoTag) {
			t.Fatalf("CID.Bytes()[0] = 0x%02x, want 0x%02x (algorithm tag was stripped)",
				cid.Bytes()[0], byte(wireFormTestAlgoTag))
		}
	})
}
