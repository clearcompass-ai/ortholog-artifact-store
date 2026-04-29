package conformance

import (
	"bytes"
	"strings"
	"testing"

	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// ─── ADR-005 §2: CID parse/serialize symmetry contract ────────────────
//
// artifact-store HTTP handlers consume the SDK's CID parse surface at
// every entry point:
//
//   - api/helpers.go parseCIDFromPath           → storage.ParseCID
//   - api/push.go    PushHandler.ServeHTTP      → storage.ParseCID
//   - api/push.go    algorithmName              → cid.String() prefix
//
// And serialize CIDs back out at every backend boundary via cid.String()
// (storage key) and cid.Bytes() (PRE Grant SplitID derivation, ADR-005
// §2). If round-trip identity (ParseCID(cid.String()) == cid) ever
// breaks, every push/resolve/fetch path silently misroutes.
//
// The wire-form scenarios already pin Bytes() preservation across a
// backend round-trip. This scenario pins the SDK's own parse/serialize
// invariants — the surface artifact-store calls hundreds of times per
// request without the bytes ever leaving the process. The scenarios
// run against every backend factory anyway because the backend's
// behavior is observable through Push+Fetch under the parsed CID.

func runCIDParse(t *testing.T, factory Factory, _ Capabilities) {
	t.Run("string_round_trip_sha256", func(t *testing.T) {
		assertStringRoundTrip(t, factory, []byte("cid-parse/string/sha256"), storage.AlgoSHA256)
	})

	t.Run("bytes_round_trip_sha256", func(t *testing.T) {
		assertBytesRoundTrip(t, factory, []byte("cid-parse/bytes/sha256"), storage.AlgoSHA256)
	})

	registerWireFormTestAlgorithm()

	t.Run("string_round_trip_custom_algo", func(t *testing.T) {
		assertStringRoundTrip(t, factory, []byte("cid-parse/string/custom"), wireFormTestAlgoTag)
	})

	t.Run("bytes_round_trip_custom_algo", func(t *testing.T) {
		assertBytesRoundTrip(t, factory, []byte("cid-parse/bytes/custom"), wireFormTestAlgoTag)
	})

	t.Run("string_has_algorithm_name_prefix", func(t *testing.T) {
		// api/push.go algorithmName reads up to the first ':' and
		// expects the algorithm registry name. Pin the shape so a
		// future SDK serialization change (e.g. multibase) gets
		// caught here, not by an unparseable audit log.
		cid := storage.ComputeWith([]byte("prefix-shape"), storage.AlgoSHA256)
		got := cid.String()
		colon := strings.IndexByte(got, ':')
		if colon <= 0 {
			t.Fatalf("CID.String() = %q: missing/leading ':' separator", got)
		}
		if got[:colon] != "sha256" {
			t.Fatalf("CID.String() prefix = %q, want %q", got[:colon], "sha256")
		}
	})

	t.Run("parse_rejects_unknown_algorithm_name", func(t *testing.T) {
		// api/push.go's PushHandler relies on ParseCID rejecting
		// unregistered algorithm names *before* the body is read.
		// If ParseCID ever silently accepts an unknown name, the
		// "invalid_cid_header" audit reason stops firing.
		_, err := storage.ParseCID("not-a-real-algorithm:0123456789abcdef")
		if err == nil {
			t.Fatal("ParseCID accepted unknown algorithm name; want error")
		}
	})

	t.Run("parse_rejects_invalid_hex", func(t *testing.T) {
		_, err := storage.ParseCID("sha256:not-hex-zzzzzzzz")
		if err == nil {
			t.Fatal("ParseCID accepted non-hex digest; want error")
		}
	})

	t.Run("parse_rejects_wrong_digest_length_for_known_algo", func(t *testing.T) {
		// SHA-256 is registered with digestSize=32. A 16-byte digest
		// under "sha256:" must be rejected — the SDK's ParseCID
		// length-checks against algorithmDigestSize, and api/push.go
		// trusts that check before its own cid.Verify() call.
		_, err := storage.ParseCID("sha256:0123456789abcdef0123456789abcdef")
		if err == nil {
			t.Fatal("ParseCID accepted 16-byte digest for sha256; want error")
		}
	})

	t.Run("parse_bytes_rejects_unknown_algorithm_tag", func(t *testing.T) {
		// algorithm tag 0xFF is not registered in any scenario in
		// this binary. ParseCIDBytes must reject — symmetric with
		// ParseCID's name-side rejection above.
		buf := append([]byte{0xFF}, bytes.Repeat([]byte{0x00}, 32)...)
		_, err := storage.ParseCIDBytes(buf)
		if err == nil {
			t.Fatal("ParseCIDBytes accepted unknown algorithm tag 0xFF; want error")
		}
	})

	t.Run("parse_bytes_rejects_short_input", func(t *testing.T) {
		_, err := storage.ParseCIDBytes([]byte{0x12}) // tag only, no digest
		if err == nil {
			t.Fatal("ParseCIDBytes accepted 1-byte input; want error")
		}
	})
}

// assertStringRoundTrip pushes data under the given algorithm, parses
// the CID's String() form back, asserts equality, and confirms the
// backend returns the same bytes when fetched through the parsed CID.
//
// The backend layer is in the loop because production handlers receive
// the CID via the HTTP path/header, parse it, and pass it to the
// backend. Any field the parser drops (algorithm tag, digest length)
// surfaces as a fetch miss — exactly the failure mode this pins.
func assertStringRoundTrip(t *testing.T, factory Factory, data []byte, algo storage.HashAlgorithm) {
	t.Helper()
	b := factory()
	original := storage.ComputeWith(data, algo)

	if err := b.Push(original, data); err != nil {
		t.Fatalf("Push: %v", err)
	}

	parsed, err := storage.ParseCID(original.String())
	if err != nil {
		t.Fatalf("ParseCID(%q): %v", original.String(), err)
	}
	if !parsed.Equal(original) {
		t.Fatalf("ParseCID round-trip drift:\n  orig=%s (algo=0x%02x len=%d)\n  parsed=%s (algo=0x%02x len=%d)",
			original.String(), byte(original.Algorithm), len(original.Digest),
			parsed.String(), byte(parsed.Algorithm), len(parsed.Digest))
	}

	got, err := b.Fetch(parsed)
	if err != nil {
		t.Fatalf("Fetch under parsed CID: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Fetch under parsed CID returned different bytes")
	}
}

// assertBytesRoundTrip mirrors assertStringRoundTrip across the
// CID.Bytes() ↔ ParseCIDBytes wire form. ADR-005 §2 mandates Bytes()
// be the input to the PRE Grant SplitID hash, so bytes-form symmetry
// is load-bearing for cross-process artifact handoff.
func assertBytesRoundTrip(t *testing.T, factory Factory, data []byte, algo storage.HashAlgorithm) {
	t.Helper()
	b := factory()
	original := storage.ComputeWith(data, algo)

	if err := b.Push(original, data); err != nil {
		t.Fatalf("Push: %v", err)
	}

	parsed, err := storage.ParseCIDBytes(original.Bytes())
	if err != nil {
		t.Fatalf("ParseCIDBytes: %v", err)
	}
	if !parsed.Equal(original) {
		t.Fatalf("ParseCIDBytes round-trip drift:\n  orig=%x\n  parsed=%x", original.Bytes(), parsed.Bytes())
	}
	if !bytes.Equal(parsed.Bytes(), original.Bytes()) {
		t.Fatalf("CID.Bytes() drift after round-trip:\n  orig=%x\n  parsed=%x", original.Bytes(), parsed.Bytes())
	}

	got, err := b.Fetch(parsed)
	if err != nil {
		t.Fatalf("Fetch under bytes-parsed CID: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Fetch under bytes-parsed CID returned different bytes")
	}
}
