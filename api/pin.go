package api

import (
	"errors"
	"net/http"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// PinHandler handles POST /v1/artifacts/{cid}/pin.
type PinHandler struct {
	backend backends.BackendProvider
}

func (h *PinHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cid, err := parseCIDFromPath(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := h.backend.Pin(cid); err != nil {
		if errors.Is(err, storage.ErrContentNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.WriteHeader(http.StatusOK)
}
