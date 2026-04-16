package api

import (
	"errors"
	"net/http"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// FetchHandler handles GET /v1/artifacts/{cid}.
// Returns raw ciphertext bytes. Cache-Control: public, immutable.
type FetchHandler struct {
	backend backends.BackendProvider
}

func (h *FetchHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cid, err := parseCIDFromPath(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	data, err := h.backend.Fetch(cid)
	if err != nil {
		if errors.Is(err, storage.ErrContentNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "public, immutable")
	w.Header().Set("Content-Length", intToStr(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
