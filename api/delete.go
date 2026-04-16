package api

import (
	"errors"
	"net/http"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// DeleteHandler handles DELETE /v1/artifacts/{cid}.
// GCS/S3: deletes the object. IPFS: returns 501 (not supported).
type DeleteHandler struct {
	backend backends.BackendProvider
}

func (h *DeleteHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cid, err := parseCIDFromPath(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := h.backend.Delete(cid); err != nil {
		if errors.Is(err, storage.ErrNotSupported) {
			writeError(w, http.StatusNotImplemented, "delete not supported by this backend")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.WriteHeader(http.StatusOK)
}
