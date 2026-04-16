package backends

import (
	"testing"

	"github.com/clearcompass-ai/ortholog-artifact-store/internal/testutil"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// FuzzExtractDigestFromIPFSCID exercises the CID parser with arbitrary
// inputs. The function is a parser on untrusted data (the IPFS Kubo RPC
// response) and must never panic. A panic here would be a DoS primitive.
//
// Run with: go test -fuzz=FuzzExtractDigestFromIPFSCID ./backends
func FuzzExtractDigestFromIPFSCID(f *testing.F) {
	// Seed with known-good inputs.
	for _, v := range testutil.KnownVectors {
		f.Add(SDKCIDToIPFSPath(v.ComputeCID()))
	}
	// Plus some adversarial seeds.
	f.Add("")
	f.Add("b")
	f.Add("bxxx")
	f.Add("zQmAbCdEfGh")
	f.Add("f0102030405060708090a0b0c0d0e0f")

	f.Fuzz(func(t *testing.T, input string) {
		// Either returns an error or a 32-byte digest. Never panics.
		digest, err := ExtractDigestFromIPFSCID(input)
		if err == nil && len(digest) != 32 {
			t.Errorf("ExtractDigestFromIPFSCID(%q) returned nil error but digest len=%d (want 32)",
				input, len(digest))
		}
	})
}

// FuzzSDKCIDToIPFSPath_RoundTrip is a property-style fuzz test: for any
// 32-byte digest, SDKCIDToIPFSPath → ExtractDigestFromIPFSCID round-trips
// identically. This is the invariant the IPFS backend depends on for every
// Push/Fetch/Pin call.
//
// Run with: go test -fuzz=FuzzSDKCIDToIPFSPath_RoundTrip ./backends
func FuzzSDKCIDToIPFSPath_RoundTrip(f *testing.F) {
	// Seed with known vectors.
	for _, v := range testutil.KnownVectors {
		f.Add(v.MustDigestBytes())
	}

	f.Fuzz(func(t *testing.T, digest []byte) {
		// Normalize to 32 bytes — anything shorter is padded with zeros,
		// anything longer is truncated. This matches the SDK's CID shape.
		var d [32]byte
		copy(d[:], digest)

		cid := storage.CID{Digest: d[:]}
		path := SDKCIDToIPFSPath(cid)

		got, err := ExtractDigestFromIPFSCID(path)
		if err != nil {
			t.Fatalf("round-trip failed: SDKCIDToIPFSPath produced %q, ExtractDigestFromIPFSCID returned %v",
				path, err)
		}
		if len(got) != 32 {
			t.Fatalf("round-trip digest wrong length: want 32, got %d", len(got))
		}
		for i := 0; i < 32; i++ {
			if got[i] != d[i] {
				t.Fatalf("round-trip digest mismatch at byte %d: want %x, got %x", i, d, got)
			}
		}
	})
}

// FuzzParseCID exercises the SDK's CID parser via the same adversarial
// input surface. This is defense in depth — if the SDK ever changes and
// introduces a panic path, this catches it in the artifact store's CI.
//
// Run with: go test -fuzz=FuzzParseCID ./backends
func FuzzParseCID(f *testing.F) {
	// Seed with real SDK CID strings.
	for _, v := range testutil.KnownVectors {
		f.Add(v.ComputeCID().String())
	}
	f.Add("")
	f.Add("sha256:")
	f.Add("sha256:not-hex")
	f.Add("sha999:" + string(make([]byte, 128)))

	f.Fuzz(func(_ *testing.T, input string) {
		// Must never panic. Error return is acceptable.
		_, _ = storage.ParseCID(input)
	})
}
