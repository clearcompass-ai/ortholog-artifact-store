package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// mkResolve wires a ResolveHandler behind a mux with a given default expiry.
func mkResolve(b backends.BackendProvider, defaultExpiry time.Duration) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /v1/artifacts/{cid}/resolve", &ResolveHandler{
		backend: b, defaultExpiry: defaultExpiry,
	})
	return mux
}

func TestResolve_HappyPath_DefaultExpiry(t *testing.T) {
	b := backends.NewInMemoryBackend()
	data := []byte("resolve-me")
	cid := storage.Compute(data)
	if err := b.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts/"+cid.String()+"/resolve", nil)
	w := httptest.NewRecorder()
	mkResolve(b, 3600*time.Second).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d; body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type: want application/json, got %q", ct)
	}

	var cred storage.RetrievalCredential
	if err := json.Unmarshal(w.Body.Bytes(), &cred); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
	if cred.Method != storage.MethodDirect {
		t.Fatalf("Method: want %s, got %s", storage.MethodDirect, cred.Method)
	}
	if cred.URL == "" {
		t.Fatal("URL is empty")
	}
}

func TestResolve_NotFound(t *testing.T) {
	b := backends.NewInMemoryBackend()
	cid := storage.Compute([]byte("nothing-here"))

	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts/"+cid.String()+"/resolve", nil)
	w := httptest.NewRecorder()
	mkResolve(b, time.Hour).ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d", w.Code)
	}
}

func TestResolve_MalformedCID(t *testing.T) {
	b := backends.NewInMemoryBackend()

	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts/junk/resolve", nil)
	w := httptest.NewRecorder()
	mkResolve(b, time.Hour).ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d", w.Code)
	}
}

// ─── ?expiry= parsing ────────────────────────────────────────────────

// expiryCapturingBackend records the expiry passed to Resolve so we can
// assert the handler parsed ?expiry= correctly.
type expiryCapturingBackend struct {
	backends.BackendProvider
	gotExpiry time.Duration
}

func (e *expiryCapturingBackend) Resolve(cid storage.CID, expiry time.Duration) (*storage.RetrievalCredential, error) {
	e.gotExpiry = expiry
	return e.BackendProvider.Resolve(cid, expiry)
}

func TestResolve_ExpiryParsing(t *testing.T) {
	cases := []struct {
		name        string
		query       string
		defaultSecs int64
		wantSecs    int64 // expected expiry passed to backend, in seconds
	}{
		{"no_query_uses_default", "", 3600, 3600},
		{"custom_valid", "?expiry=60", 3600, 60},
		{"invalid_string_falls_back", "?expiry=abc", 3600, 3600},
		{"negative_falls_back", "?expiry=-1", 3600, 3600},
		{"zero_falls_back", "?expiry=0", 3600, 3600},
		{"explicit_large", "?expiry=604800", 3600, 604800}, // 7 days
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			base := backends.NewInMemoryBackend()
			data := []byte("expiry-test")
			cid := storage.Compute(data)
			if err := base.Push(cid, data); err != nil {
				t.Fatalf("setup Push: %v", err)
			}
			capturing := &expiryCapturingBackend{BackendProvider: base}

			mux := http.NewServeMux()
			mux.Handle("GET /v1/artifacts/{cid}/resolve", &ResolveHandler{
				backend:       capturing,
				defaultExpiry: time.Duration(tc.defaultSecs) * time.Second,
			})

			req := httptest.NewRequest(http.MethodGet,
				"/v1/artifacts/"+cid.String()+"/resolve"+tc.query, nil)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status: want 200, got %d; body=%s", w.Code, w.Body.String())
			}
			want := time.Duration(tc.wantSecs) * time.Second
			if capturing.gotExpiry != want {
				t.Fatalf("expiry passed to backend: want %v, got %v", want, capturing.gotExpiry)
			}
		})
	}
}

// ─── Backend error propagation ───────────────────────────────────────

type resolveErrorBackend struct{ backends.BackendProvider }

func (r resolveErrorBackend) Exists(_ storage.CID) (bool, error) { return true, nil }
func (r resolveErrorBackend) Resolve(_ storage.CID, _ time.Duration) (*storage.RetrievalCredential, error) {
	return nil, errWrapGeneric
}

func TestResolve_BackendError(t *testing.T) {
	b := resolveErrorBackend{BackendProvider: backends.NewInMemoryBackend()}
	cid := storage.Compute([]byte("x"))

	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts/"+cid.String()+"/resolve", nil)
	w := httptest.NewRecorder()
	mkResolve(b, time.Hour).ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: want 500, got %d", w.Code)
	}
}

// existsErrorBackend tests the existence-precheck error path.
type existsErrorBackend struct{ backends.BackendProvider }

func (e existsErrorBackend) Exists(_ storage.CID) (bool, error) {
	return false, errWrapGeneric
}

func TestResolve_ExistsBackendError(t *testing.T) {
	b := existsErrorBackend{BackendProvider: backends.NewInMemoryBackend()}
	cid := storage.Compute([]byte("x"))

	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts/"+cid.String()+"/resolve", nil)
	w := httptest.NewRecorder()
	mkResolve(b, time.Hour).ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: want 500, got %d", w.Code)
	}
}
