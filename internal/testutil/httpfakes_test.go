package testutil

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

// ─── GCS fake ────────────────────────────────────────────────────────

func TestGCSFake_URLNonEmpty(t *testing.T) {
	f := NewGCSFake(t)
	if f.URL() == "" {
		t.Fatal("URL: want non-empty, got empty")
	}
	if !strings.HasPrefix(f.URL(), "http://") {
		t.Fatalf("URL: want http:// prefix, got %q", f.URL())
	}
}

func TestGCSFake_PushAndFetchRoundTrip(t *testing.T) {
	f := NewGCSFake(t)
	body := []byte("gcs-fake-body")

	pushURL := f.URL() + "/upload/storage/v1/b/test-bucket/o?uploadType=media&name=k"
	resp := mustDo(t, http.MethodPost, pushURL, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Push: want 200, got %d", resp.StatusCode)
	}

	fetchURL := f.URL() + "/storage/v1/b/test-bucket/o/k?alt=media"
	resp = mustDo(t, http.MethodGet, fetchURL, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Fetch: want 200, got %d", resp.StatusCode)
	}
	got := mustReadAll(t, resp.Body)
	if !bytes.Equal(got, body) {
		t.Fatalf("byte mismatch: want %q got %q", body, got)
	}
}

func TestGCSFake_FetchMissingReturns404(t *testing.T) {
	f := NewGCSFake(t)
	resp := mustDo(t, http.MethodGet, f.URL()+"/storage/v1/b/test-bucket/o/missing?alt=media", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("Fetch missing: want 404, got %d", resp.StatusCode)
	}
}

func TestGCSFake_PreloadedPutVisibleViaFetch(t *testing.T) {
	f := NewGCSFake(t)
	f.Put("preload-bucket", "object-x", []byte("preloaded"))

	resp := mustDo(t, http.MethodGet, f.URL()+"/storage/v1/b/preload-bucket/o/object-x?alt=media", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Fetch preloaded: want 200, got %d", resp.StatusCode)
	}
	got := mustReadAll(t, resp.Body)
	if string(got) != "preloaded" {
		t.Fatalf("preloaded fetch byte mismatch: %q", got)
	}
}

func TestGCSFake_ExistsHEAD200WhenPresent_404WhenAbsent(t *testing.T) {
	f := NewGCSFake(t)
	f.Put("b", "k", []byte("x"))

	if resp := mustDo(t, http.MethodGet, f.URL()+"/storage/v1/b/b/o/k", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("Exists present: want 200, got %d", resp.StatusCode)
	}
	if resp := mustDo(t, http.MethodGet, f.URL()+"/storage/v1/b/b/o/missing", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("Exists absent: want 404, got %d", resp.StatusCode)
	}
}

func TestGCSFake_DeleteRemoves(t *testing.T) {
	f := NewGCSFake(t)
	f.Put("b", "k", []byte("x"))

	if resp := mustDo(t, http.MethodDelete, f.URL()+"/storage/v1/b/b/o/k", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("Delete: want 200, got %d", resp.StatusCode)
	}
	if resp := mustDo(t, http.MethodGet, f.URL()+"/storage/v1/b/b/o/k?alt=media", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("Fetch after Delete: want 404, got %d", resp.StatusCode)
	}
}

func TestGCSFake_HealthyOK(t *testing.T) {
	f := NewGCSFake(t)
	resp := mustDo(t, http.MethodGet, f.URL()+"/storage/v1/b/anything", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Healthy: want 200, got %d", resp.StatusCode)
	}
}

func TestGCSFake_StatusOverrides(t *testing.T) {
	f := NewGCSFake(t)
	f.PushStatus = http.StatusInternalServerError
	resp := mustDo(t, http.MethodPost, f.URL()+"/upload/storage/v1/b/b/o?uploadType=media&name=k",
		[]byte("body"))
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("PushStatus override: want 500, got %d", resp.StatusCode)
	}
}

func TestGCSFake_RequestsObservation(t *testing.T) {
	f := NewGCSFake(t)
	body := []byte("trace")
	mustDo(t, http.MethodPost, f.URL()+"/upload/storage/v1/b/b/o?uploadType=media&name=k", body)

	reqs := f.Requests()
	if len(reqs) != 1 {
		t.Fatalf("Requests: want 1, got %d", len(reqs))
	}
	r := reqs[0]
	if r.Method != http.MethodPost {
		t.Errorf("Method: want POST, got %s", r.Method)
	}
	if !strings.Contains(r.Path, "/upload/storage/v1/b/b/o") {
		t.Errorf("Path: want upload path, got %s", r.Path)
	}
	if !bytes.Equal(r.Body, body) {
		t.Errorf("Body: want %q, got %q", body, r.Body)
	}
	// Header is captured (Cloned). Just verify it's non-nil.
	if r.Header == nil {
		t.Error("Header: want non-nil clone, got nil")
	}
	// Re-call Requests; the second call must also return (a snapshot)
	// — proves Requests isn't draining the slice.
	if len(f.Requests()) != 1 {
		t.Fatal("Requests must be a snapshot, not a drain")
	}
}

func TestGCSFake_UnknownPathFallsThroughToGenericHandler(t *testing.T) {
	// The fake's handle() dispatch covers known prefixes; an unknown
	// path returns a non-200 (depending on internal logic). Just
	// verify the call completes without panic and the request is
	// recorded.
	f := NewGCSFake(t)
	mustDo(t, http.MethodGet, f.URL()+"/this/is/unrouted", nil)
	reqs := f.Requests()
	if len(reqs) != 1 {
		t.Fatalf("Requests: want 1, got %d", len(reqs))
	}
}

// ─── S3 fake ─────────────────────────────────────────────────────────

func TestS3Fake_URLNonEmpty(t *testing.T) {
	f := NewS3Fake(t)
	if f.URL() == "" {
		t.Fatal("URL: want non-empty, got empty")
	}
}

func TestS3Fake_PushAndFetchRoundTrip(t *testing.T) {
	f := NewS3Fake(t)
	body := []byte("s3-fake-body")

	pushURL := f.URL() + "/test-bucket/object-key"
	resp := mustDo(t, http.MethodPut, pushURL, body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("Push: want 200/201, got %d", resp.StatusCode)
	}

	resp = mustDo(t, http.MethodGet, f.URL()+"/test-bucket/object-key", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Fetch: want 200, got %d", resp.StatusCode)
	}
	got := mustReadAll(t, resp.Body)
	if !bytes.Equal(got, body) {
		t.Fatalf("byte mismatch: %q vs %q", got, body)
	}
}

func TestS3Fake_FetchMissingReturns404(t *testing.T) {
	f := NewS3Fake(t)
	resp := mustDo(t, http.MethodGet, f.URL()+"/test-bucket/missing", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("Fetch missing: want 404, got %d", resp.StatusCode)
	}
}

func TestS3Fake_PreloadedPutVisibleViaFetch(t *testing.T) {
	f := NewS3Fake(t)
	f.Put("preload-bucket", "k", []byte("preloaded-s3"))
	resp := mustDo(t, http.MethodGet, f.URL()+"/preload-bucket/k", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Fetch preloaded: want 200, got %d", resp.StatusCode)
	}
}

func TestS3Fake_ExistsHEAD(t *testing.T) {
	f := NewS3Fake(t)
	f.Put("b", "k", []byte("x"))
	if resp := mustDo(t, http.MethodHead, f.URL()+"/b/k", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD present: want 200, got %d", resp.StatusCode)
	}
	if resp := mustDo(t, http.MethodHead, f.URL()+"/b/missing", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("HEAD absent: want 404, got %d", resp.StatusCode)
	}
}

func TestS3Fake_DeleteRemoves(t *testing.T) {
	f := NewS3Fake(t)
	f.Put("b", "k", []byte("x"))
	if resp := mustDo(t, http.MethodDelete, f.URL()+"/b/k", nil); resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("Delete: want 200/204, got %d", resp.StatusCode)
	}
	if resp := mustDo(t, http.MethodGet, f.URL()+"/b/k", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("Fetch after Delete: want 404, got %d", resp.StatusCode)
	}
}

func TestS3Fake_TaggingPin(t *testing.T) {
	// PUT /<bucket>/<key>?tagging is the pin shape used by RustFSBackend.
	// The fake records the request; the body is the XML Tagging payload.
	f := NewS3Fake(t)
	f.Put("b", "k", []byte("x"))
	tagBody := `<?xml version="1.0"?><Tagging><TagSet><Tag><Key>ortholog-pinned</Key><Value>true</Value></Tag></TagSet></Tagging>`
	resp := mustDo(t, http.MethodPut, f.URL()+"/b/k?tagging", []byte(tagBody))
	if resp.StatusCode >= 400 {
		t.Fatalf("Pin tagging: want <400, got %d", resp.StatusCode)
	}
	// Verify the request was observed with ?tagging.
	found := false
	for _, r := range f.Requests() {
		if r.Method == http.MethodPut && strings.Contains(r.Query, "tagging") {
			found = true
		}
	}
	if !found {
		t.Fatal("no tagging request observed")
	}
}

func TestS3Fake_StatusOverrides(t *testing.T) {
	f := NewS3Fake(t)
	f.FetchStatus = http.StatusForbidden
	f.Put("b", "k", []byte("x"))
	resp := mustDo(t, http.MethodGet, f.URL()+"/b/k", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("FetchStatus override: want 403, got %d", resp.StatusCode)
	}
}

func TestS3Fake_HealthyOK(t *testing.T) {
	f := NewS3Fake(t)
	// HEAD on bucket root.
	resp := mustDo(t, http.MethodHead, f.URL()+"/test-bucket", nil)
	if resp.StatusCode >= 500 {
		t.Fatalf("Healthy: want <500, got %d", resp.StatusCode)
	}
}

func TestS3Fake_RequestsObservation(t *testing.T) {
	f := NewS3Fake(t)
	body := []byte("trace")
	mustDo(t, http.MethodPut, f.URL()+"/b/k", body)

	reqs := f.Requests()
	if len(reqs) != 1 {
		t.Fatalf("Requests: want 1, got %d", len(reqs))
	}
	r := reqs[0]
	if r.Method != http.MethodPut {
		t.Errorf("Method: want PUT, got %s", r.Method)
	}
	if !bytes.Equal(r.Body, body) {
		t.Errorf("Body: want %q, got %q", body, r.Body)
	}
}

// ─── Test helpers ────────────────────────────────────────────────────

func mustDo(t *testing.T, method, url string, body []byte) *http.Response {
	t.Helper()
	var br *bytes.Reader
	if body != nil {
		br = bytes.NewReader(body)
	}
	var req *http.Request
	var err error
	if br != nil {
		req, err = http.NewRequest(method, url, br)
	} else {
		req, err = http.NewRequest(method, url, nil)
	}
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do %s %s: %v", method, url, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func mustReadAll(t *testing.T, r io.Reader) []byte {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return b
}
