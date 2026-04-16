package backends

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/clearcompass-ai/ortholog-artifact-store/internal/testutil"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// newIPFSBackendWithFake wires the IPFSBackend at the fake Kubo server.
func newIPFSBackendWithFake(t *testing.T, fake *testutil.KuboFake, token string) *IPFSBackend {
	t.Helper()
	return NewIPFSBackend(IPFSConfig{
		APIEndpoint: fake.URL(),
		Gateway:     "https://gateway.example",
		BearerToken: token,
	})
}

// ─── Push: wire format + digest verification ─────────────────────────

func TestIPFS_Push_SendsMultipart(t *testing.T) {
	fake := testutil.NewKuboFake(t)
	b := newIPFSBackendWithFake(t, fake, "")
	data := []byte("ipfs-push-payload")
	cid := storage.Compute(data)

	if err := b.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}

	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("want 1 request, got %d", len(reqs))
	}
	r := reqs[0]

	if r.Method != http.MethodPost {
		t.Errorf("method: want POST, got %s", r.Method)
	}
	if r.Path != "/api/v0/add" {
		t.Errorf("path: want /api/v0/add, got %s", r.Path)
	}
	// Kubo RPC path forcing CIDv1 + raw leaves + sha2-256 + pin=true.
	wantParams := []string{"cid-version=1", "raw-leaves=true", "hash=sha2-256", "pin=true"}
	for _, p := range wantParams {
		if !strings.Contains(r.Query, p) {
			t.Errorf("query missing %q: %s", p, r.Query)
		}
	}
	ct := r.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "multipart/form-data") {
		t.Errorf("Content-Type: want multipart/form-data..., got %q", ct)
	}
}

func TestIPFS_Push_AcceptsValidCIDResponse(t *testing.T) {
	fake := testutil.NewKuboFake(t)
	b := newIPFSBackendWithFake(t, fake, "")
	data := []byte("valid-cid-accept")
	cid := storage.Compute(data)

	// Fake produces a valid CIDv1 for the exact bytes — digest match path.
	if err := b.Push(cid, data); err != nil {
		t.Fatalf("Push with valid response: %v", err)
	}
}

func TestIPFS_Push_RejectsDigestMismatch(t *testing.T) {
	fake := testutil.NewKuboFake(t)
	fake.CorruptAddCID = true // fake returns CID for wrong bytes
	b := newIPFSBackendWithFake(t, fake, "")

	data := []byte("honest-payload")
	cid := storage.Compute(data)

	err := b.Push(cid, data)
	if err == nil {
		t.Fatal("Push with mismatched CID response: want error, got nil")
	}
	if !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("error should mention digest mismatch: %v", err)
	}
	if !strings.Contains(err.Error(), "data corruption in transit") {
		t.Fatalf("error should mention 'data corruption in transit': %v", err)
	}
}

func TestIPFS_Push_HTTP500ReturnsError(t *testing.T) {
	fake := testutil.NewKuboFake(t)
	fake.AddStatus = http.StatusInternalServerError
	b := newIPFSBackendWithFake(t, fake, "")

	err := b.Push(storage.Compute([]byte("x")), []byte("x"))
	if err == nil {
		t.Fatal("Push on 500: want error, got nil")
	}
}

// ─── Bearer token forwarding ─────────────────────────────────────────

func TestIPFS_BearerTokenForwarded(t *testing.T) {
	fake := testutil.NewKuboFake(t)
	b := newIPFSBackendWithFake(t, fake, "secret-token-123")

	data := []byte("auth")
	cid := storage.Compute(data)
	if err := b.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}

	reqs := fake.Requests()
	got := reqs[0].Header.Get("Authorization")
	want := "Bearer secret-token-123"
	if got != want {
		t.Fatalf("Authorization: want %q, got %q", want, got)
	}
}

func TestIPFS_NoBearerToken_NoAuthHeader(t *testing.T) {
	fake := testutil.NewKuboFake(t)
	b := newIPFSBackendWithFake(t, fake, "")

	if err := b.Healthy(); err != nil {
		t.Fatalf("Healthy: %v", err)
	}
	reqs := fake.Requests()
	if got := reqs[0].Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization must be absent, got %q", got)
	}
}

// ─── Fetch ───────────────────────────────────────────────────────────

func TestIPFS_Fetch_RoundTrip(t *testing.T) {
	fake := testutil.NewKuboFake(t)
	b := newIPFSBackendWithFake(t, fake, "")

	data := []byte("ipfs-fetch-test")
	cid := storage.Compute(data)
	if err := b.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}
	got, err := b.Fetch(cid)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("bytes mismatch:\n  want=%x\n  got =%x", data, got)
	}
}

// Kubo returns 500 with "block was not found" in the body for missing
// content. This test pins that error-classification behavior.
func TestIPFS_Fetch_BlockNotFoundMapsToErrContentNotFound(t *testing.T) {
	fake := testutil.NewKuboFake(t)
	b := newIPFSBackendWithFake(t, fake, "")
	cid := storage.Compute([]byte("never-pushed"))

	_, err := b.Fetch(cid)
	if !errors.Is(err, storage.ErrContentNotFound) {
		t.Fatalf("Fetch missing: want ErrContentNotFound, got %v", err)
	}
}

// ─── Exists (via pin/ls) ─────────────────────────────────────────────

func TestIPFS_Exists_TrueForPinned(t *testing.T) {
	fake := testutil.NewKuboFake(t)
	b := newIPFSBackendWithFake(t, fake, "")
	data := []byte("pinned")
	cid := storage.Compute(data)
	if err := b.Push(cid, data); err != nil { // Push auto-pins
		t.Fatalf("Push: %v", err)
	}

	exists, err := b.Exists(cid)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatal("Exists returned false for pinned object")
	}
}

func TestIPFS_Exists_FalseForUnpinned(t *testing.T) {
	fake := testutil.NewKuboFake(t)
	b := newIPFSBackendWithFake(t, fake, "")

	exists, err := b.Exists(storage.Compute([]byte("nope")))
	if err != nil {
		t.Fatalf("Exists missing: %v", err)
	}
	if exists {
		t.Fatal("Exists true for unpinned")
	}
}

// ─── Pin ─────────────────────────────────────────────────────────────

func TestIPFS_Pin_AddsToPinset(t *testing.T) {
	fake := testutil.NewKuboFake(t)
	b := newIPFSBackendWithFake(t, fake, "")
	data := []byte("pin-me")
	cid := storage.Compute(data)
	if err := b.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if err := b.Pin(cid); err != nil {
		t.Fatalf("Pin: %v", err)
	}
}

// ─── Delete returns ErrNotSupported ──────────────────────────────────

func TestIPFS_Delete_AlwaysReturnsErrNotSupported(t *testing.T) {
	fake := testutil.NewKuboFake(t)
	b := newIPFSBackendWithFake(t, fake, "")

	err := b.Delete(storage.Compute([]byte("x")))
	if !errors.Is(err, storage.ErrNotSupported) {
		t.Fatalf("Delete: want ErrNotSupported, got %v", err)
	}
}

// ─── Resolve ─────────────────────────────────────────────────────────

func TestIPFS_Resolve_ReturnsMethodIPFSWithNilExpiry(t *testing.T) {
	fake := testutil.NewKuboFake(t)
	b := newIPFSBackendWithFake(t, fake, "")
	data := []byte("resolve")
	cid := storage.Compute(data)
	if err := b.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}

	// expiry is passed in but must be ignored by IPFS — the gateway URL
	// is permanent.
	cred, err := b.Resolve(cid, 3600*time.Second)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cred.Method != storage.MethodIPFS {
		t.Fatalf("Method: want %s, got %s", storage.MethodIPFS, cred.Method)
	}
	if cred.Expiry != nil {
		t.Fatalf("IPFS Resolve.Expiry must be nil (gateway URL is permanent), got %v", *cred.Expiry)
	}
	if !strings.HasPrefix(cred.URL, "https://gateway.example/ipfs/") {
		t.Fatalf("URL: want gateway prefix, got %s", cred.URL)
	}
}

// ─── Healthy ─────────────────────────────────────────────────────────

func TestIPFS_Healthy_OK(t *testing.T) {
	fake := testutil.NewKuboFake(t)
	b := newIPFSBackendWithFake(t, fake, "")
	if err := b.Healthy(); err != nil {
		t.Fatalf("Healthy: %v", err)
	}
}

func TestIPFS_Healthy_500ReturnsError(t *testing.T) {
	fake := testutil.NewKuboFake(t)
	fake.IDStatus = http.StatusInternalServerError
	b := newIPFSBackendWithFake(t, fake, "")
	if err := b.Healthy(); err == nil {
		t.Fatal("Healthy 500: want error, got nil")
	}
}

// ─── CID codec round-trips (SDKCIDToIPFSPath / ExtractDigestFromIPFSCID) ─

func TestIPFS_CIDCodec_RoundTrip(t *testing.T) {
	// For every known vector, SDKCIDToIPFSPath produces a CIDv1 string
	// that ExtractDigestFromIPFSCID parses back to the original digest.
	for _, v := range testutil.KnownVectors {
		v := v
		t.Run(v.Name, func(t *testing.T) {
			cid := v.ComputeCID()
			ipfsPath := SDKCIDToIPFSPath(cid)

			if !strings.HasPrefix(ipfsPath, "b") {
				t.Fatalf("IPFS path must start with 'b' (base32lower): %s", ipfsPath)
			}

			gotDigest, err := ExtractDigestFromIPFSCID(ipfsPath)
			if err != nil {
				t.Fatalf("ExtractDigest: %v", err)
			}
			if !bytes.Equal(gotDigest, cid.Digest) {
				t.Fatalf("digest round-trip mismatch:\n  want=%x\n  got =%x", cid.Digest, gotDigest)
			}
		})
	}
}

func TestIPFS_ExtractDigest_RejectsShortInput(t *testing.T) {
	cases := []string{"", "b", "bx", "bxxxxxxxxxxxxxxxx"}
	for _, tc := range cases {
		tc := tc
		t.Run("input_"+tc, func(t *testing.T) {
			if _, err := ExtractDigestFromIPFSCID(tc); err == nil {
				t.Fatalf("expected error for input %q, got nil", tc)
			}
		})
	}
}

func TestIPFS_ExtractDigest_RejectsUnknownMultibase(t *testing.T) {
	// 'z' is base58, not supported by our backend.
	_, err := ExtractDigestFromIPFSCID("zQm1234567890")
	if err == nil {
		t.Fatal("expected error for unsupported multibase prefix")
	}
	if !strings.Contains(err.Error(), "unsupported multibase prefix") {
		t.Fatalf("error message should mention multibase: %v", err)
	}
}

func TestIPFS_ExtractDigest_RejectsInvalidBase32Chars(t *testing.T) {
	// Uppercase is not part of base32-lower alphabet.
	_, err := ExtractDigestFromIPFSCID("bABCDEFGHIJKLMNOPQRSTUVWXYZ234567")
	if err == nil {
		t.Fatal("expected error for invalid base32 char")
	}
}
