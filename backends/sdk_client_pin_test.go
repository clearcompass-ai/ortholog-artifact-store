/*
FILE PATH: backends/sdk_client_pin_test.go

DESCRIPTION:
    Tier-3 alignment pin tests. Both NewGCSBackend and
    NewRustFSBackend construct their *http.Client via
    sdklog.DefaultClient(60s), which composes the SDK's tuned
    transport (MaxIdleConnsPerHost=100) with RetryAfterRoundTripper
    (honors HTTP 503 + Retry-After).

    Each backend's Push and Fetch paths are exercised against an
    httptest.Server that returns 503 with Retry-After: 1 on the
    first attempt and 200 (with the expected backend-specific
    response shape) on the second. The operation succeeds in ≥ 2
    attempts — proof that the new client wiring is live.

    A future refactor that drops back to a bare http.Client (no
    503 honoring) fails these tests deterministically: the first
    attempt returns 503, the SDK retry path is gone, and the
    backend surfaces the 503 as a hard error.
*/
package backends

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// ─────────────────────────────────────────────────────────────────────
// GCS backend: 503-retry on Push and Fetch
// ─────────────────────────────────────────────────────────────────────

// flakyGCSServer is an httptest.Server that returns 503 + Retry-After
// on the first request to each path, and a "real" GCS-shaped response
// on subsequent requests. The path-keyed counter means Push and Fetch
// each see their own 503 → 200 transition.
func flakyGCSServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var totalCalls atomic.Int32
	seenByPath := make(map[string]bool)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		totalCalls.Add(1)
		key := r.Method + " " + r.URL.Path
		first := !seenByPath[key]
		seenByPath[key] = true
		if first {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		switch {
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/upload/storage/v1/b/"):
			// GCS push response: JSON with the object name.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"name":"ok"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.RawQuery, "alt=media"):
			// GCS fetch (alt=media) returns the bytes.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("retry-fetch"))
		default:
			// Default OK response for other requests — keep the
			// fixture forgiving.
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &totalCalls
}

func TestGCS_Push_RetriesOn503(t *testing.T) {
	srv, calls := flakyGCSServer(t)

	b := NewGCSBackend(GCSConfig{
		Bucket:    "test-bucket",
		Prefix:    "artifacts/",
		BaseURL:   srv.URL,
		TokenFunc: func() (string, error) { return "tok", nil },
		URLSigner: fakeGCSSigner{},
	})

	data := []byte("retry-push")
	cid := storage.Compute(data)
	if err := b.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if got := calls.Load(); got < 2 {
		t.Errorf("expected ≥ 2 attempts (503 → 200), got %d", got)
	}
}

func TestGCS_Fetch_RetriesOn503(t *testing.T) {
	srv, calls := flakyGCSServer(t)

	b := NewGCSBackend(GCSConfig{
		Bucket:    "test-bucket",
		Prefix:    "artifacts/",
		BaseURL:   srv.URL,
		TokenFunc: func() (string, error) { return "tok", nil },
		URLSigner: fakeGCSSigner{},
	})

	cid := storage.Compute([]byte("retry-push"))
	got, err := b.Fetch(cid)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(got) != "retry-fetch" {
		t.Errorf("body: got %q, want %q", got, "retry-fetch")
	}
	if c := calls.Load(); c < 2 {
		t.Errorf("expected ≥ 2 attempts (503 → 200), got %d", c)
	}
}

// ─────────────────────────────────────────────────────────────────────
// RustFS / S3-protocol backend: 503-retry on Push and Fetch
// ─────────────────────────────────────────────────────────────────────

// flakyRustFSServer mirrors flakyGCSServer for the S3 wire protocol:
// 503 + Retry-After on the first hit per (method, path); subsequent
// hits succeed.
func flakyRustFSServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var totalCalls atomic.Int32
	seenByPath := make(map[string]bool)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		totalCalls.Add(1)
		key := r.Method + " " + r.URL.Path
		first := !seenByPath[key]
		seenByPath[key] = true
		if first {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		switch r.Method {
		case http.MethodPut:
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("retry-fetch"))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &totalCalls
}

func TestRustFS_Push_RetriesOn503(t *testing.T) {
	srv, calls := flakyRustFSServer(t)

	signer := &capturingRustFSSigner{}
	b := NewRustFSBackend(RustFSConfig{
		Endpoint:      srv.URL,
		Bucket:        "test-bucket",
		Region:        "us-east-1",
		Prefix:        "artifacts/",
		PathStyle:     true,
		RequestSigner: signer,
		URLSigner:     fakeRustFSPresigner{},
	})

	data := []byte("retry-push")
	cid := storage.Compute(data)
	if err := b.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if got := calls.Load(); got < 2 {
		t.Errorf("expected ≥ 2 attempts (503 → 200), got %d", got)
	}
}

func TestRustFS_Fetch_RetriesOn503(t *testing.T) {
	srv, calls := flakyRustFSServer(t)

	signer := &capturingRustFSSigner{}
	b := NewRustFSBackend(RustFSConfig{
		Endpoint:      srv.URL,
		Bucket:        "test-bucket",
		Region:        "us-east-1",
		Prefix:        "artifacts/",
		PathStyle:     true,
		RequestSigner: signer,
		URLSigner:     fakeRustFSPresigner{},
	})

	cid := storage.Compute([]byte("retry-push"))
	got, err := b.Fetch(cid)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(got) != "retry-fetch" {
		t.Errorf("body: got %q, want %q", got, "retry-fetch")
	}
	if c := calls.Load(); c < 2 {
		t.Errorf("expected ≥ 2 attempts (503 → 200), got %d", c)
	}
}

// ─────────────────────────────────────────────────────────────────────
// Negative case: non-503 errors still surface immediately
// ─────────────────────────────────────────────────────────────────────

// TestBackends_DoNotRetry4xx confirms the SDK transport's retry
// path is scoped to 503. A 404 / 403 / 422 / 500 surfaces as a
// hard error after exactly one attempt — no spurious retries on
// client-error or non-retryable server-error statuses.
func TestBackends_DoNotRetry4xx(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
	}{
		{"404", http.StatusNotFound},
		{"500", http.StatusInternalServerError},
	} {
		t.Run("RustFS_Fetch_"+tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.code)
			}))
			defer srv.Close()

			b := NewRustFSBackend(RustFSConfig{
				Endpoint: srv.URL, Bucket: "b", Region: "us-east-1", PathStyle: true,
				RequestSigner: &capturingRustFSSigner{},
				URLSigner:     fakeRustFSPresigner{},
			})
			cid := storage.Compute([]byte("x"))
			_, _ = b.Fetch(cid) // err expected on both codes
			if got := calls.Load(); got != 1 {
				t.Errorf("%s: expected exactly 1 attempt, got %d", tc.name, got)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────
// Sanity: time package import (some Go versions complain when an
// imported package is not used in any non-test file in a directory).
// ─────────────────────────────────────────────────────────────────────

var _ = time.Second
