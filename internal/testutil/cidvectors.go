package testutil

import (
	"encoding/hex"
	"math/rand"

	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// KnownVector is a plaintext→CID pair used to lock the SDK's CID
// computation. If the SDK ever silently changes its hash algorithm or
// multihash encoding, these tests break loudly. The digests here are
// raw SHA-256 of the plaintext — the only algorithm the SDK currently
// supports and the one IPFS CIDv1+raw+sha2-256 produces for the same bytes.
type KnownVector struct {
	Name       string
	Plaintext  []byte
	DigestHex  string // hex-encoded raw SHA-256(plaintext)
}

// KnownVectors are stable across the SDK's lifetime (as long as SHA-256
// remains the CID algorithm). Tests use them for:
//   - Asserting storage.Compute(v.Plaintext).Digest == hex(DigestHex)
//   - Asserting storage.ParseCID(Compute(...).String()) round-trips
//   - Exercising small/medium/exact-32-byte/edge-case bodies
//
// Digests were computed offline via: echo -n <plaintext> | sha256sum
var KnownVectors = []KnownVector{
	{
		Name:      "empty",
		Plaintext: []byte(""),
		// SHA-256 of the empty string.
		DigestHex: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	},
	{
		Name:      "single-byte-zero",
		Plaintext: []byte{0x00},
		DigestHex: "6e340b9cffb37a989ca544e6bb780a2c78901d3fb33738768511a30617afa01d",
	},
	{
		Name:      "single-byte-ff",
		Plaintext: []byte{0xff},
		DigestHex: "a8100ae6aa1940d0b663bb31cd466142ebbdbd5187131b92d93818987832eb89",
	},
	{
		Name:      "ascii-hello",
		Plaintext: []byte("hello"),
		DigestHex: "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824",
	},
	{
		Name:      "ortholog-preamble",
		Plaintext: []byte("ortholog-artifact-store-wave-1"),
		// Computed in init() so we don't have to maintain another offline vector.
		DigestHex: "", // filled in init()
	},
}

func init() {
	// Fill in the computed digest for the preamble vector.
	for i, v := range KnownVectors {
		if v.DigestHex == "" {
			KnownVectors[i].DigestHex = hex.EncodeToString(hashSHA256(v.Plaintext))
		}
	}
}

// MustDigestBytes returns the raw digest bytes from the hex representation.
// Panics on malformed hex — the fixtures are checked in, so a panic here
// indicates corruption of this file rather than a test input error.
func (v KnownVector) MustDigestBytes() []byte {
	b, err := hex.DecodeString(v.DigestHex)
	if err != nil {
		panic("malformed KnownVector.DigestHex for " + v.Name + ": " + err.Error())
	}
	return b
}

// ComputeCID returns the SDK's CID for this vector. Convenience wrapper.
func (v KnownVector) ComputeCID() storage.CID {
	return storage.Compute(v.Plaintext)
}

// DeterministicBytes returns n bytes from a seeded PRNG. Tests that need
// larger bodies (approaching MaxBodySize, etc.) use this to get the same
// bytes every run without hardcoding megabytes in the repo.
//
// The seed is the caller's responsibility — use the same seed across runs
// to get identical bodies.
//
//nolint:gosec // G404: cryptographic randomness is not required for test data.
func DeterministicBytes(seed int64, n int) []byte {
	rng := rand.New(rand.NewSource(seed))
	out := make([]byte, n)
	_, _ = rng.Read(out)
	return out
}
