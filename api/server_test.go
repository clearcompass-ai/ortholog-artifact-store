package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-artifact-store/internal/testutil"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// TestMux_WiresAllRoutes hits every route through the mux and verifies
// it was wired. This catches regressions where someone adds a handler
// file but forgets to register it in NewMux.
func TestMux_WiresAllRoutes(t *testing.T) {
	b := backends.NewInMemoryBackend()
	data := []byte("route-test")
	cid := storage.Compute(data)
	if err := b.Push(cid, data); err != nil {
		t.Fatalf("setup Push: %v", err)
	}

	cap := testutil.NewSlogCapture()
	mux := NewMux(ServerConfig{
		Backend:       b,
		VerifyOnPush:  true,
		MaxBodySize:   1024,
		DefaultExpiry: time.Hour,
		Logger:        cap.Logger(),
	})

	cases := []struct {
		name     string
		method   string
		path     string
		body     []byte
		header   map[string]string
		wantCode int
	}{
		{"POST_push", http.MethodPost, "/v1/artifacts", data,
			map[string]string{"X-Artifact-CID": cid.String()}, http.StatusOK},
		{"GET_fetch", http.MethodGet, "/v1/artifacts/" + cid.String(), nil, nil, http.StatusOK},
		{"HEAD_exists", http.MethodHead, "/v1/artifacts/" + cid.String(), nil, nil, http.StatusOK},
		{"GET_resolve", http.MethodGet, "/v1/artifacts/" + cid.String() + "/resolve", nil, nil, http.StatusOK},
		{"POST_pin", http.MethodPost, "/v1/artifacts/" + cid.String() + "/pin", nil, nil, http.StatusOK},
		{"DELETE", http.MethodDelete, "/v1/artifacts/" + cid.String(), nil, nil, http.StatusOK},
		{"GET_healthz", http.MethodGet, "/healthz", nil, nil, http.StatusOK},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var req *http.Request
			if tc.body != nil {
				req = httptest.NewRequest(tc.method, tc.path, bytes.NewReader(tc.body))
			} else {
				req = httptest.NewRequest(tc.method, tc.path, nil)
			}
			for k, v := range tc.header {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != tc.wantCode {
				t.Fatalf("%s %s: want %d, got %d; body=%s",
					tc.method, tc.path, tc.wantCode, w.Code, w.Body.String())
			}
		})
	}
}

// TestMux_LoggerFallback_NoCrash verifies that when ServerConfig.Logger
// is nil, NewMux falls back to slog.Default() and handlers still work.
// This is the path existing callers (e.g., older integration tests) use.
func TestMux_LoggerFallback_NoCrash(t *testing.T) {
	// Route a pushed artifact through a mux with nil Logger.
	b := backends.NewInMemoryBackend()
	mux := NewMux(ServerConfig{
		Backend:      b,
		VerifyOnPush: true,
		MaxBodySize:  1024,
		Logger:       nil, // <- the point of the test
	})

	data := []byte("no-logger-provided")
	cid := storage.Compute(data)
	req := httptest.NewRequest(http.MethodPost, "/v1/artifacts", bytes.NewReader(data))
	req.Header.Set("X-Artifact-CID", cid.String())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200 (logger fallback should not fail), got %d", w.Code)
	}
}

// TestMux_404ForUnknownRoute confirms the mux doesn't silently accept
// unknown paths — a common source of "my handler isn't being called"
// debugging sessions.
func TestMux_404ForUnknownRoute(t *testing.T) {
	mux := NewMux(ServerConfig{
		Backend: backends.NewInMemoryBackend(),
		Logger:  slog.Default(),
	})

	req := httptest.NewRequest(http.MethodGet, "/not-a-real-route", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown route: want 404, got %d", w.Code)
	}
}

// TestMux_MethodMismatch tests that GET on a POST-only route returns
// 405 (or equivalent behavior from net/http.ServeMux).
func TestMux_MethodMismatch(t *testing.T) {
	mux := NewMux(ServerConfig{
		Backend: backends.NewInMemoryBackend(),
		Logger:  slog.Default(),
	})
	// /v1/artifacts is POST-only.
	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	// Go 1.22 ServeMux returns 405 for method mismatch on a registered pattern.
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method mismatch: want 405, got %d", w.Code)
	}
}
