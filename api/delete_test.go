package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

func mkDelete(b backends.BackendProvider) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("DELETE /v1/artifacts/{cid}", &DeleteHandler{backend: b})
	return mux
}

func TestDelete_Success(t *testing.T) {
	b := backends.NewInMemoryBackend()
	data := []byte("delete")
	cid := storage.Compute(data)
	if err := b.Push(cid, data); err != nil {
		t.Fatalf("setup Push: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/v1/artifacts/"+cid.String(), nil)
	w := httptest.NewRecorder()
	mkDelete(b).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
	exists, _ := b.Exists(cid)
	if exists {
		t.Fatal("Exists true after Delete")
	}
}

func TestDelete_MalformedCID(t *testing.T) {
	b := backends.NewInMemoryBackend()

	req := httptest.NewRequest(http.MethodDelete, "/v1/artifacts/nope", nil)
	w := httptest.NewRecorder()
	mkDelete(b).ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d", w.Code)
	}
}

// deleteUnsupportedBackend returns storage.ErrNotSupported — the IPFS case.
// The handler must translate it to HTTP 501 Not Implemented.
type deleteUnsupportedBackend struct{ backends.BackendProvider }

func (d deleteUnsupportedBackend) Delete(_ storage.CID) error {
	return storage.ErrNotSupported
}

func TestDelete_NotSupportedReturns501(t *testing.T) {
	b := deleteUnsupportedBackend{BackendProvider: backends.NewInMemoryBackend()}
	cid := storage.Compute([]byte("x"))

	req := httptest.NewRequest(http.MethodDelete, "/v1/artifacts/"+cid.String(), nil)
	w := httptest.NewRecorder()
	mkDelete(b).ServeHTTP(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status: want 501, got %d", w.Code)
	}
}

// deleteErrorBackend — arbitrary backend error → 500.
type deleteErrorBackend struct{ backends.BackendProvider }

func (d deleteErrorBackend) Delete(_ storage.CID) error {
	return errWrapGeneric
}

func TestDelete_BackendError(t *testing.T) {
	b := deleteErrorBackend{BackendProvider: backends.NewInMemoryBackend()}
	cid := storage.Compute([]byte("x"))

	req := httptest.NewRequest(http.MethodDelete, "/v1/artifacts/"+cid.String(), nil)
	w := httptest.NewRecorder()
	mkDelete(b).ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: want 500, got %d", w.Code)
	}
}
