package backends

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/clearcompass-ai/ortholog-artifact-store/internal/testutil"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// ─── Fake URL signer ─────────────────────────────────────────────────

// fakeGCSSigner returns a deterministic URL so tests can assert on its
// shape without having to mock a real GCS signer with a private key.
type fakeGCSSigner struct{}

func (fakeGCSSigner) SignURL(bucket, object string, expiry time.Duration) (string, error) {
	return fmt.Sprintf("https://signed.example/%s/%s?e=%d",
		bucket, object, int(expiry.Seconds())), nil
}

// brokenGCSSigner exercises the error path in Resolve.
type brokenGCSSigner struct{}

func (brokenGCSSigner) SignURL(_, _ string, _ time.Duration) (string, error) {
	return "", errors.New("signer exploded")
}

// newGCSBackendWithFake wires a GCSBackend at the fake server's URL
// using a static bearer token and the fake signer.
func newGCSBackendWithFake(t *testing.T, fake *testutil.GCSFake) *GCSBackend {
	t.Helper()
	return NewGCSBackend(GCSConfig{
		Bucket:    "test-bucket",
		Prefix:    "artifacts/",
		BaseURL:   fake.URL(),
		TokenFunc: func() (string, error) { return "test-token-abcdef", nil },
		URLSigner: fakeGCSSigner{},
	})
}

// ─── Happy-path wire-format tests ────────────────────────────────────

func TestGCS_Push_SendsExactRequestShape(t *testing.T) {
	fake := testutil.NewGCSFake(t)
	b := newGCSBackendWithFake(t, fake)

	data := []byte("gcs-push-wire-check")
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
	if !strings.HasPrefix(r.Path, "/upload/storage/v1/b/test-bucket/o") {
		t.Errorf("path: want /upload/storage/v1/b/test-bucket/o..., got %s", r.Path)
	}
	// The push URL must URL-encode the object name in the ?name=
	// value: "/" → "%2F", ":" → "%3A". Without this, GCS path-form
	// reads (Fetch / Delete / Exists) silently 404 against the
	// pushed object — see the URL helper docstring in gcs.go.
	wantName := url.QueryEscape("artifacts/" + cid.String())
	if !strings.Contains(r.Query, "name="+wantName) {
		t.Errorf("query: want name=%s, got %s", wantName, r.Query)
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type: want application/octet-stream, got %q", ct)
	}
	if auth := r.Header.Get("Authorization"); auth != "Bearer test-token-abcdef" {
		t.Errorf("Authorization: want 'Bearer test-token-abcdef', got %q", auth)
	}
	if !bytes.Equal(r.Body, data) {
		t.Errorf("body bytes mismatch: want %x, got %x", data, r.Body)
	}
}

func TestGCS_Fetch_ReturnsStoredBytes(t *testing.T) {
	fake := testutil.NewGCSFake(t)
	b := newGCSBackendWithFake(t, fake)

	data := []byte("gcs-fetch")
	cid := storage.Compute(data)
	// Preload via Put so Fetch has something to retrieve.
	fake.Put("test-bucket", "artifacts/"+cid.String(), data)

	got, err := b.Fetch(cid)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("bytes mismatch:\n  want=%x\n  got =%x", data, got)
	}
}

func TestGCS_Fetch_404MapsToErrContentNotFound(t *testing.T) {
	fake := testutil.NewGCSFake(t)
	b := newGCSBackendWithFake(t, fake)
	cid := storage.Compute([]byte("missing"))

	_, err := b.Fetch(cid)
	if !errors.Is(err, storage.ErrContentNotFound) {
		t.Fatalf("Fetch missing: want ErrContentNotFound, got %v", err)
	}
}

func TestGCS_Fetch_500ReturnsError(t *testing.T) {
	fake := testutil.NewGCSFake(t)
	fake.FetchStatus = http.StatusInternalServerError
	b := newGCSBackendWithFake(t, fake)
	cid := storage.Compute([]byte("x"))

	_, err := b.Fetch(cid)
	if err == nil {
		t.Fatal("Fetch 500: want error, got nil")
	}
	if errors.Is(err, storage.ErrContentNotFound) {
		t.Fatal("Fetch 500 must NOT be reported as not-found")
	}
}

func TestGCS_Exists_TrueOnObject(t *testing.T) {
	fake := testutil.NewGCSFake(t)
	b := newGCSBackendWithFake(t, fake)
	cid := storage.Compute([]byte("exists-yes"))
	fake.Put("test-bucket", "artifacts/"+cid.String(), []byte("x"))

	exists, err := b.Exists(cid)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatal("Exists returned false for preloaded object")
	}
}

func TestGCS_Exists_FalseOnMissing(t *testing.T) {
	fake := testutil.NewGCSFake(t)
	b := newGCSBackendWithFake(t, fake)
	cid := storage.Compute([]byte("exists-no"))

	exists, err := b.Exists(cid)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Fatal("Exists returned true for missing object")
	}
}

func TestGCS_Pin_SendsPatchWithMetadata(t *testing.T) {
	fake := testutil.NewGCSFake(t)
	b := newGCSBackendWithFake(t, fake)
	data := []byte("pin")
	cid := storage.Compute(data)
	fake.Put("test-bucket", "artifacts/"+cid.String(), data)

	if err := b.Pin(cid); err != nil {
		t.Fatalf("Pin: %v", err)
	}

	// Find the PATCH request among observed requests.
	var patched bool
	for _, r := range fake.Requests() {
		if r.Method == http.MethodPatch {
			patched = true
			if ct := r.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("Pin Content-Type: want application/json, got %q", ct)
			}
			if !strings.Contains(string(r.Body), "ortholog-pinned") {
				t.Errorf("Pin body missing 'ortholog-pinned' marker: %s", r.Body)
			}
		}
	}
	if !patched {
		t.Fatal("no PATCH request observed for Pin")
	}
}

func TestGCS_Pin_MissingReturnsErrContentNotFound(t *testing.T) {
	fake := testutil.NewGCSFake(t)
	b := newGCSBackendWithFake(t, fake)
	cid := storage.Compute([]byte("pin-missing"))

	err := b.Pin(cid)
	if !errors.Is(err, storage.ErrContentNotFound) {
		t.Fatalf("Pin missing: want ErrContentNotFound, got %v", err)
	}
}

func TestGCS_Delete_SendsDELETE(t *testing.T) {
	fake := testutil.NewGCSFake(t)
	b := newGCSBackendWithFake(t, fake)
	data := []byte("delete")
	cid := storage.Compute(data)
	fake.Put("test-bucket", "artifacts/"+cid.String(), data)

	if err := b.Delete(cid); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Last observed request must be DELETE with correct path.
	reqs := fake.Requests()
	last := reqs[len(reqs)-1]
	if last.Method != http.MethodDelete {
		t.Fatalf("last method: want DELETE, got %s", last.Method)
	}
	if !strings.Contains(last.Path, "artifacts/"+cid.String()) {
		t.Fatalf("delete path missing CID: %s", last.Path)
	}
}

func TestGCS_Healthy_OK(t *testing.T) {
	fake := testutil.NewGCSFake(t)
	b := newGCSBackendWithFake(t, fake)
	if err := b.Healthy(); err != nil {
		t.Fatalf("Healthy: %v", err)
	}
}

func TestGCS_Healthy_500ReturnsError(t *testing.T) {
	fake := testutil.NewGCSFake(t)
	fake.HealthyStatus = http.StatusInternalServerError
	b := newGCSBackendWithFake(t, fake)
	if err := b.Healthy(); err == nil {
		t.Fatal("Healthy 500: want error, got nil")
	}
}

// ─── Resolve ─────────────────────────────────────────────────────────

func TestGCS_Resolve_ReturnsMethodSignedURLWithExpiry(t *testing.T) {
	fake := testutil.NewGCSFake(t)
	b := newGCSBackendWithFake(t, fake)
	cid := storage.Compute([]byte("resolve"))

	before := time.Now()
	cred, err := b.Resolve(cid, 3600*time.Second)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cred.Method != storage.MethodSignedURL {
		t.Fatalf("Method: want %s, got %s", storage.MethodSignedURL, cred.Method)
	}
	if cred.URL == "" {
		t.Fatal("URL empty")
	}
	if !strings.Contains(cred.URL, "test-bucket") || !strings.Contains(cred.URL, cid.String()) {
		t.Fatalf("URL missing bucket or CID: %s", cred.URL)
	}
	if cred.Expiry == nil {
		t.Fatal("Expiry must be non-nil for signed URL")
	}
	if cred.Expiry.Before(before.Add(3500 * time.Second)) {
		t.Fatalf("Expiry too early: want ≥ %v, got %v", before.Add(3500*time.Second), *cred.Expiry)
	}
}

func TestGCS_Resolve_NoSignerReturnsError(t *testing.T) {
	b := NewGCSBackend(GCSConfig{Bucket: "t", BaseURL: "http://unused"})
	_, err := b.Resolve(storage.Compute([]byte("x")), time.Hour)
	if err == nil {
		t.Fatal("Resolve without signer: want error, got nil")
	}
}

func TestGCS_Resolve_SignerErrorPropagates(t *testing.T) {
	b := NewGCSBackend(GCSConfig{
		Bucket:    "t",
		BaseURL:   "http://unused",
		URLSigner: brokenGCSSigner{},
	})
	_, err := b.Resolve(storage.Compute([]byte("x")), time.Hour)
	if err == nil {
		t.Fatal("Resolve with broken signer: want error, got nil")
	}
	if !strings.Contains(err.Error(), "signer exploded") {
		t.Fatalf("signer error not propagated: %v", err)
	}
}

// ─── Token-less backend ──────────────────────────────────────────────

// ─── Object-name URL encoding (regression) ──────────────────────────
//
// These tests pin the behavior that all GCS path/query URL helpers
// URL-encode the object name. Without encoding, real-cloud GCS (and
// any conformant implementation) silently 404s Fetch / Delete /
// Exists / Pin against a pushed object whose name contains "/" or
// ":". The artifact store's CID always contains ":" (the algorithm
// prefix), and any non-empty Prefix that itself contains "/" forces
// "/" into the object name. The scale test (tests/staging) was the
// first caller to combine both — it caught the bug live, which is
// what this regression test prevents from recurring.

func TestGCS_URLEncoding_PathSegmentEscapesSlashAndColon(t *testing.T) {
	fake := testutil.NewGCSFake(t)
	// Slashy prefix + CID with ":" — the same shape the staging
	// scale test produces (staging/<test>/<nonce>/sha256:<hex>).
	prefix := "staging/scale/abcd1234/"
	b := NewGCSBackend(GCSConfig{
		Bucket:    "test-bucket",
		Prefix:    prefix,
		BaseURL:   fake.URL(),
		TokenFunc: func() (string, error) { return "tok", nil },
	})
	data := []byte("encoded-path-roundtrip")
	cid := storage.Compute(data)

	if err := b.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}
	got, err := b.Fetch(cid)
	if err != nil {
		t.Fatalf("Fetch after Push: %v (this would 404 against real GCS without URL encoding)", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Fetch returned wrong bytes")
	}

	// Inspect the wire form. The Fetch request line must carry the
	// encoded form (not the raw "/" and ":" in the path), because
	// real GCS rejects the raw form.
	reqs := fake.Requests()
	var fetchReq testutil.ObservedRequest
	for _, r := range reqs {
		if r.Method == http.MethodGet && strings.Contains(r.RawPath, "/o/") {
			fetchReq = r
		}
	}
	wantSegment := url.PathEscape(prefix + cid.String())
	if !strings.Contains(fetchReq.RawPath, wantSegment) {
		t.Errorf("fetch raw path: want encoded segment %q in %q",
			wantSegment, fetchReq.RawPath)
	}
	// Belt-and-suspenders: the raw path must NOT contain the
	// unencoded prefix (which would be the bug).
	if strings.Contains(fetchReq.RawPath, prefix+cid.String()) {
		t.Errorf("fetch raw path contains unencoded object name: %q",
			fetchReq.RawPath)
	}
}

func TestGCS_Delete_SurfacesNon2xx(t *testing.T) {
	// Before the fix Delete returned nil regardless of status,
	// silently masking 4xx/5xx (and the URL-encoding 404 bug above).
	// This test pins the new contract: 404 → ErrContentNotFound,
	// other non-2xx → wrapped error, 2xx → nil.
	fake := testutil.NewGCSFake(t)
	fake.DeleteStatus = http.StatusForbidden
	b := newGCSBackendWithFake(t, fake)
	cid := storage.Compute([]byte("x"))

	err := b.Delete(cid)
	if err == nil {
		t.Fatal("Delete on 403: want error, got nil (the swallow-all bug returned)")
	}
	if errors.Is(err, storage.ErrContentNotFound) {
		t.Fatalf("Delete 403 must NOT be reported as not-found: %v", err)
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("Delete error should mention status; got: %v", err)
	}
}

func TestGCS_Delete_404MapsToErrContentNotFound(t *testing.T) {
	fake := testutil.NewGCSFake(t)
	fake.DeleteStatus = http.StatusNotFound
	b := newGCSBackendWithFake(t, fake)
	cid := storage.Compute([]byte("missing"))

	err := b.Delete(cid)
	if !errors.Is(err, storage.ErrContentNotFound) {
		t.Fatalf("Delete 404: want ErrContentNotFound, got %v", err)
	}
}

func TestGCS_NoTokenFunc_NoAuthHeader(t *testing.T) {
	// A backend constructed without TokenFunc should omit Authorization.
	// This is important for anonymous-bucket scenarios.
	fake := testutil.NewGCSFake(t)
	b := NewGCSBackend(GCSConfig{
		Bucket:  "test-bucket",
		Prefix:  "artifacts/",
		BaseURL: fake.URL(),
	})

	data := []byte("no-token")
	cid := storage.Compute(data)
	if err := b.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}

	reqs := fake.Requests()
	if got := reqs[0].Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization should be absent when no TokenFunc, got %q", got)
	}
}
