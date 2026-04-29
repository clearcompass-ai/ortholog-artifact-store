package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// ResolveHandler handles GET /v1/artifacts/{cid}/resolve.
// Passthrough: calls backend.Resolve and returns the credential.
// The Method field comes from the backend (storage.MethodSignedURL,
// storage.MethodIPFS, or storage.MethodDirect). No switch, no mapping.
type ResolveHandler struct {
	backend       backends.BackendProvider
	defaultExpiry time.Duration
}

func (h *ResolveHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cid, err := parseCIDFromPath(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Parse expiry from query parameter (seconds). Falls back to default.
	expiry := h.defaultExpiry
	if es := r.URL.Query().Get("expiry"); es != "" {
		if secs, err := strconv.ParseInt(es, 10, 64); err == nil && secs > 0 {
			expiry = time.Duration(secs) * time.Second
		}
	}

	// Check existence first (cheaper than signing a URL for nothing).
	exists, err := h.backend.Exists(cid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	credential, err := h.backend.Resolve(cid, expiry)
	if err != nil {
		if errors.Is(err, storage.ErrContentNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(retrievalCredentialToWire(credential))
}
