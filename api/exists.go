package api

import (
	"net/http"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
)

// ExistsHandler handles HEAD /v1/artifacts/{cid}.
// Returns 200 (exists) or 404 (not found). No body.
type ExistsHandler struct {
	backend backends.BackendProvider
}

func (h *ExistsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cid, err := parseCIDFromPath(r)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	exists, err := h.backend.Exists(cid)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if exists {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusNotFound)
	}
}
