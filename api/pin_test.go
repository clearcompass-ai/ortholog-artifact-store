package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

func mkPin(b backends.BackendProvider) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /v1/artifacts/{cid}/pin", &PinHandler{backend: b})
	return mux
}

func TestPin_Success(t *testing.T) {
	b := backends.NewInMemoryBackend()
	data := []byte("pin-me")
	cid := storage.Compute(data)
	if err := b.Push(cid, data); err != nil {
		t.Fatalf("setup Push: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/artifacts/"+cid.String()+"/pin", nil)
	w := httptest.NewRecorder()
	mkPin(b).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
}

func TestPin_MalformedCID(t *testing.T) {
	b := backends.NewInMemoryBackend()

	req := httptest.NewRequest(http.MethodPost, "/v1/artifacts/nope/pin", nil)
	w := httptest.NewRecorder()
	mkPin(b).ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d", w.Code)
	}
}

type pinNotFoundBackend struct{ backends.BackendProvider }

func (p pinNotFoundBackend) Pin(_ storage.CID) error { return storage.ErrContentNotFound }

func TestPin_NotFoundReturns404(t *testing.T) {
	b := pinNotFoundBackend{BackendProvider: backends.NewInMemoryBackend()}
	cid := storage.Compute([]byte("missing"))

	req := httptest.NewRequest(http.MethodPost, "/v1/artifacts/"+cid.String()+"/pin", nil)
	w := httptest.NewRecorder()
	mkPin(b).ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d", w.Code)
	}
}

type pinErrorBackend struct{ backends.BackendProvider }

func (p pinErrorBackend) Pin(_ storage.CID) error { return errWrapGeneric }

func TestPin_BackendError(t *testing.T) {
	b := pinErrorBackend{BackendProvider: backends.NewInMemoryBackend()}
	cid := storage.Compute([]byte("x"))

	req := httptest.NewRequest(http.MethodPost, "/v1/artifacts/"+cid.String()+"/pin", nil)
	w := httptest.NewRecorder()
	mkPin(b).ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: want 500, got %d", w.Code)
	}
}
