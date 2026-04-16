package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

func mkExists(b backends.BackendProvider) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("HEAD /v1/artifacts/{cid}", &ExistsHandler{backend: b})
	return mux
}

func TestExists_Present(t *testing.T) {
	b := backends.NewInMemoryBackend()
	data := []byte("here")
	cid := storage.Compute(data)
	if err := b.Push(cid, data); err != nil {
		t.Fatalf("setup Push: %v", err)
	}

	req := httptest.NewRequest(http.MethodHead, "/v1/artifacts/"+cid.String(), nil)
	w := httptest.NewRecorder()
	mkExists(b).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("HEAD response must have empty body, got %d bytes", w.Body.Len())
	}
}

func TestExists_Absent(t *testing.T) {
	b := backends.NewInMemoryBackend()
	cid := storage.Compute([]byte("nope"))

	req := httptest.NewRequest(http.MethodHead, "/v1/artifacts/"+cid.String(), nil)
	w := httptest.NewRecorder()
	mkExists(b).ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d", w.Code)
	}
}

func TestExists_MalformedCID(t *testing.T) {
	b := backends.NewInMemoryBackend()

	req := httptest.NewRequest(http.MethodHead, "/v1/artifacts/xyz", nil)
	w := httptest.NewRecorder()
	mkExists(b).ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d", w.Code)
	}
}

// existsFailBackend exercises the 500 path when the backend errors.
type existsFailBackend struct{ backends.BackendProvider }

func (f existsFailBackend) Exists(_ storage.CID) (bool, error) {
	return false, errWrapGeneric
}

func TestExists_BackendError(t *testing.T) {
	b := existsFailBackend{BackendProvider: backends.NewInMemoryBackend()}
	cid := storage.Compute([]byte("x"))

	req := httptest.NewRequest(http.MethodHead, "/v1/artifacts/"+cid.String(), nil)
	w := httptest.NewRecorder()
	mkExists(b).ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: want 500, got %d", w.Code)
	}
}
