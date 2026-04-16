package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
)

func TestHealth_Healthy(t *testing.T) {
	h := &HealthHandler{backend: backends.NewInMemoryBackend()}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type: want application/json, got %q", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if body["status"] != "healthy" {
		t.Fatalf("status field: want healthy, got %q", body["status"])
	}
}

type unhealthyBackend struct{ backends.BackendProvider }

func (u unhealthyBackend) Healthy() error { return errWrapGeneric }

func TestHealth_Unhealthy(t *testing.T) {
	h := &HealthHandler{backend: unhealthyBackend{BackendProvider: backends.NewInMemoryBackend()}}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: want 503, got %d", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if body["status"] != "unhealthy" {
		t.Fatalf("status field: want unhealthy, got %q", body["status"])
	}
	if body["error"] == "" {
		t.Fatal("error field should be populated")
	}
}
