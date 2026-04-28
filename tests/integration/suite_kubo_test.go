//go:build integration

package integration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-artifact-store/tests/conformance"
	"github.com/clearcompass-ai/ortholog-artifact-store/tests/integration/containers"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// TestConformance_Kubo runs the conformance suite against a real Kubo
// daemon. This is the single most valuable test for IPFS — the HTTP
// mock in backends/ipfs_test.go can't validate the actual multihash
// encoding, block retrieval semantics, or pin set behavior of a real
// go-ipfs node.
func TestConformance_Kubo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	k := containers.StartKubo(t, ctx)

	conformance.RunBackendConformance(t, "kubo",
		func() backends.BackendProvider {
			return backends.NewIPFSBackend(backends.IPFSConfig{
				APIEndpoint: k.APIEndpoint,
				Gateway:     k.Gateway,
				// Kubo's default RPC requires no bearer token.
			})
		},
		conformance.Capabilities{
			SupportsDelete:        false, // Kubo has best-effort GC, not delete
			SupportsExpiry:        false, // IPFS gateway URLs are permanent
			ExpectedResolveMethod: storage.MethodIPFS,
			SHA256Only:            true, // IPFS multihash 0x12; non-SHA-256 → ErrNotSupported
		},
	)
}

// TestKubo_DeleteAlwaysUnsupported pins the Delete contract against a
// real daemon. Wave 1's HTTP mock asserts the backend returns
// storage.ErrNotSupported; Wave 2 asserts this holds against the real
// protocol regardless of what Kubo happens to do — there's no way to
// surface "deleted" in IPFS, so the backend must not pretend otherwise.
func TestKubo_DeleteAlwaysUnsupported(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	k := containers.StartKubo(t, ctx)
	b := backends.NewIPFSBackend(backends.IPFSConfig{
		APIEndpoint: k.APIEndpoint,
		Gateway:     k.Gateway,
	})

	data := []byte("delete-attempt")
	cid := storage.Compute(data)
	if err := b.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if err := b.Delete(cid); !errors.Is(err, storage.ErrNotSupported) {
		t.Fatalf("Delete: want ErrNotSupported, got %v", err)
	}
}

// TestKubo_PushThenFetchRoundTrip verifies the full push/fetch cycle
// end-to-end. A failure here points at wire-format drift between our
// client and real Kubo — exactly what Wave 2 exists to catch.
func TestKubo_PushThenFetchRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	k := containers.StartKubo(t, ctx)
	b := backends.NewIPFSBackend(backends.IPFSConfig{
		APIEndpoint: k.APIEndpoint,
		Gateway:     k.Gateway,
	})

	data := []byte("real-kubo-round-trip")
	cid := storage.Compute(data)
	if err := b.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}
	got, err := b.Fetch(cid)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("round-trip mismatch:\n  want=%x\n  got =%x", data, got)
	}
}

// TestKubo_CIDDigestMatchesSDK is the most important Wave 2 test: real
// Kubo returns a CIDv1 string that decodes to the same 32-byte SHA-256
// digest the SDK computed. This is the property the push handler's
// "digest mismatch (data corruption in transit)" check relies on. If
// the encoding ever drifts — alphabet, multibase prefix, multicodec —
// this test catches it before production.
func TestKubo_CIDDigestMatchesSDK(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	k := containers.StartKubo(t, ctx)
	b := backends.NewIPFSBackend(backends.IPFSConfig{
		APIEndpoint: k.APIEndpoint,
		Gateway:     k.Gateway,
	})

	// The backend computes the expected digest from SDK cid; Kubo's /add
	// response produces the IPFS CID; the backend extracts the digest and
	// compares. If Push returns nil error, the match held. This test
	// exercises the path with several different payload sizes to shake
	// out any size-dependent encoding edge case.
	for _, size := range []int{0, 1, 32, 1024, 65536} {
		size := size
		t.Run(fmt.Sprintf("size=%d", size), func(t *testing.T) {
			data := make([]byte, size)
			for i := range data {
				data[i] = byte(i & 0xff)
			}
			cid := storage.Compute(data)
			if err := b.Push(cid, data); err != nil {
				t.Fatalf("Push size=%d: %v", size, err)
			}
		})
	}
}
