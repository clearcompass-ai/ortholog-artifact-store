package api

import (
	"encoding/json"
	"net/http"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
)

// HealthHandler handles GET /healthz.
type HealthHandler struct {
	backend backends.BackendProvider
}

func (h *HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	err := h.backend.Healthy()
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "unhealthy",
			"error":  err.Error(),
		})
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
}
