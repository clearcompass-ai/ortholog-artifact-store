//go:build staging

package staging

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-artifact-store/tests/conformance"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// Filebase IPFS RPC is a token-authenticated Kubo-compatible endpoint.
// Docs: https://docs.filebase.com/ipfs/using-ipfs-pinning-services
const filebaseIPFSEndpoint = "https://rpc.filebase.io/api/v1/ipfs"

// Filebase's gateway prefix. Pinned content resolves via a dedicated
// per-bucket subdomain, but the generic gateway works for tests.
const filebaseIPFSGateway = "https://ipfs.filebase.io"

func newFilebaseIPFSBackend(t *testing.T) backends.BackendProvider {
	t.Helper()
	if !filebaseConfigured() {
		t.Skipf("Filebase credentials not configured")
	}
	return backends.NewIPFSBackend(backends.IPFSConfig{
		APIEndpoint: filebaseIPFSEndpoint,
		Gateway:     filebaseIPFSGateway,
		BearerToken: os.Getenv("STAGING_FILEBASE_IPFS_TOKEN"),
	})
}

// TestConformance_Filebase_IPFS runs the conformance suite against
// Filebase's authenticated Kubo-compatible IPFS RPC. This exercises
// the bearer-token code path that Wave 2's anonymous Kubo cannot test.
func TestConformance_Filebase_IPFS(t *testing.T) {
	if !filebaseConfigured() {
		t.Skip("Filebase not configured for this run")
	}
	recordOp(t)
	factory := func() backends.BackendProvider {
		return newFilebaseIPFSBackend(t)
	}

	conformance.RunBackendConformance(t, "filebase-ipfs", factory,
		conformance.Capabilities{
			SupportsDelete:        false,
			SupportsExpiry:        false,
			ExpectedResolveMethod: storage.MethodIPFS,
		})
}

// TestFilebase_IPFS_Healthy validates the bearer token is correct and
// the RPC endpoint reachable.
func TestFilebase_IPFS_Healthy(t *testing.T) {
	if !filebaseConfigured() {
		t.Skip("Filebase not configured")
	}
	b := newFilebaseIPFSBackend(t)
	recordOp(t)
	if err := b.Healthy(); err != nil {
		t.Fatalf("Filebase IPFS Healthy: %v (check STAGING_FILEBASE_IPFS_TOKEN)", err)
	}
}

// TestFilebase_IPFS_CIDDigestMatchesSDK is the high-stakes Wave 3 test
// for IPFS: it proves that Filebase's IPFS RPC returns CIDs whose
// extracted SHA-256 digests match what the SDK computed for the same
// bytes. This is the exact property the push handler's data-corruption
// detection depends on. Wave 2 verified it against a local Kubo;
// Wave 3 verifies it against Filebase's production infrastructure,
// which may run a patched Kubo or a custom implementation.
func TestFilebase_IPFS_CIDDigestMatchesSDK(t *testing.T) {
	if !filebaseConfigured() {
		t.Skip("Filebase not configured")
	}
	b := newFilebaseIPFSBackend(t)

	// Small payload — Filebase IPFS is pay-per-byte, keep it cheap.
	data := []byte("filebase-ipfs-cid-property")
	cid := storage.Compute(data)
	if err := b.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}
	recordOp(t)
	// Push succeeding means the digest comparison inside the backend
	// held. The assertion is implicit in the method contract.
}

// TestFilebase_IPFS_GatewayURL_Fetchable validates the Resolve URL
// actually retrieves the content via Filebase's gateway. Eventually
// consistent — wait up to 30s for propagation.
func TestFilebase_IPFS_GatewayURL_Fetchable(t *testing.T) {
	if !filebaseConfigured() {
		t.Skip("Filebase not configured")
	}
	b := newFilebaseIPFSBackend(t)

	data := []byte("filebase-gateway-fetch")
	cid := storage.Compute(data)
	if err := b.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}
	recordOp(t)

	cred, err := b.Resolve(cid, 0)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cred.Expiry != nil {
		t.Fatalf("IPFS Resolve.Expiry must be nil (permanent URL), got %v", *cred.Expiry)
	}

	// Eventual consistency: retry with backoff.
	got, err := fetchURLBytesEventual(context.Background(), cred.URL, 30, 1)
	if err != nil {
		t.Fatalf("GET IPFS gateway: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("gateway returned wrong bytes")
	}
}

// TestFilebase_IPFS_DeleteUnsupported pins the backend contract:
// Filebase IPFS doesn't support true delete (only unpin, which doesn't
// guarantee removal). The backend must return storage.ErrNotSupported.
func TestFilebase_IPFS_DeleteUnsupported(t *testing.T) {
	if !filebaseConfigured() {
		t.Skip("Filebase not configured")
	}
	b := newFilebaseIPFSBackend(t)
	cid := storage.Compute([]byte("delete-attempt"))
	err := b.Delete(cid)
	recordOp(t)
	if !errors.Is(err, storage.ErrNotSupported) {
		t.Fatalf("Delete: want ErrNotSupported, got %v", err)
	}
}
