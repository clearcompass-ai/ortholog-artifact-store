package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// mkFetch wires a FetchHandler behind a mux so {cid} PathValue resolves.
func mkFetch(b backends.BackendProvider) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /v1/artifacts/{cid}", &FetchHandler{backend: b})
	return mux
}

func TestFetch_HappyPath(t *testing.T) {
	b := backends.NewInMemoryBackend()
	data := []byte("fetched")
	cid := storage.Compute(data)
	if err := b.Push(cid, data); err != nil {
		t.Fatalf("setup Push: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts/"+cid.String(), nil)
	w := httptest.NewRecorder()
	mkFetch(b).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d; body=%s", w.Code, w.Body.String())
	}
	if got := w.Body.Bytes(); !bytes.Equal(got, data) {
		t.Fatalf("body bytes mismatch:\n  want=%x\n  got =%x", data, got)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("Content-Type: want application/octet-stream, got %q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "public, immutable" {
		t.Fatalf("Cache-Control: want 'public, immutable', got %q", cc)
	}
	if cl := w.Header().Get("Content-Length"); cl != "7" { // len("fetched") = 7
		t.Fatalf("Content-Length: want 7, got %q", cl)
	}
}

func TestFetch_NotFound(t *testing.T) {
	b := backends.NewInMemoryBackend()
	cid := storage.Compute([]byte("never-pushed"))

	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts/"+cid.String(), nil)
	w := httptest.NewRecorder()
	mkFetch(b).ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d", w.Code)
	}
}

func TestFetch_MalformedCID(t *testing.T) {
	b := backends.NewInMemoryBackend()

	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts/not-a-cid", nil)
	w := httptest.NewRecorder()
	mkFetch(b).ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d", w.Code)
	}
}

// Backend returning a generic error (not ErrContentNotFound) → 500.
type fetchFailBackend struct{ backends.BackendProvider }

func (f fetchFailBackend) Fetch(_ storage.CID) ([]byte, error) {
	return nil, errWrapGeneric
}

func TestFetch_BackendError(t *testing.T) {
	b := fetchFailBackend{BackendProvider: backends.NewInMemoryBackend()}

	cid := storage.Compute([]byte("will-fail"))
	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts/"+cid.String(), nil)
	w := httptest.NewRecorder()
	mkFetch(b).ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: want 500, got %d", w.Code)
	}
}

// errWrapGeneric is a shared sentinel for "backend blew up in some way
// that isn't ErrContentNotFound or ErrNotSupported." Tests across
// multiple handler files reuse it to avoid declaring their own.
var errWrapGeneric = &genericError{msg: "synthetic backend failure"}

type genericError struct{ msg string }

func (e *genericError) Error() string { return e.msg }
