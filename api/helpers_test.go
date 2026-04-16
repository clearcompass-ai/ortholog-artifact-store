package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// parseCIDFromPath needs a real *http.Request with a PathValue set, so
// we drive it through a ServeMux route that wires {cid} into the path
// value store. This mirrors how the real handlers receive the CID.
func TestParseCIDFromPath_Valid(t *testing.T) {
	cid := storage.Compute([]byte("some-bytes"))
	var got storage.CID
	var gotErr error

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/artifacts/{cid}", func(_ http.ResponseWriter, r *http.Request) {
		got, gotErr = parseCIDFromPath(r)
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts/"+cid.String(), nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if gotErr != nil {
		t.Fatalf("unexpected error: %v", gotErr)
	}
	if got.String() != cid.String() {
		t.Fatalf("CID mismatch:\n  want=%s\n  got =%s", cid.String(), got.String())
	}
}

func TestParseCIDFromPath_Missing(t *testing.T) {
	// Hit a route that doesn't have {cid} at all — PathValue returns "".
	mux := http.NewServeMux()
	var err error
	mux.HandleFunc("GET /", func(_ http.ResponseWriter, r *http.Request) {
		_, err = parseCIDFromPath(r)
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	mux.ServeHTTP(httptest.NewRecorder(), req)

	if err == nil || !strings.Contains(err.Error(), "missing CID") {
		t.Fatalf("want 'missing CID' error, got %v", err)
	}
}

func TestParseCIDFromPath_Malformed(t *testing.T) {
	mux := http.NewServeMux()
	var err error
	mux.HandleFunc("GET /v1/artifacts/{cid}", func(_ http.ResponseWriter, r *http.Request) {
		_, err = parseCIDFromPath(r)
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts/not-a-valid-cid", nil)
	mux.ServeHTTP(httptest.NewRecorder(), req)

	if err == nil {
		t.Fatal("expected error for malformed CID, got nil")
	}
}

// writeError must always emit valid JSON and set Content-Type correctly.
// A handler that emits invalid JSON on error is worse than one that
// returns text, because clients parse it and crash downstream.
func TestWriteError_EmitsJSON(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, http.StatusBadRequest, "something bad")

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type: want application/json, got %q", ct)
	}

	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("body is not valid JSON: %v; raw=%s", err, w.Body.String())
	}
	if body["error"] != "something bad" {
		t.Fatalf("body: want {error: something bad}, got %v", body)
	}
}

func TestWriteError_StatusCodes(t *testing.T) {
	// Verify a sample of status codes the handlers emit.
	for _, code := range []int{400, 404, 413, 500, 503} {
		code := code
		t.Run(fmt.Sprintf("status_%d", code), func(t *testing.T) {
			w := httptest.NewRecorder()
			writeError(w, code, "msg")
			if w.Code != code {
				t.Fatalf("status: want %d, got %d", code, w.Code)
			}
		})
	}
}

func TestIntToStr(t *testing.T) {
	cases := map[int]string{
		0: "0", 1: "1", 42: "42", -1: "-1", 1024: "1024",
	}
	for in, want := range cases {
		if got := intToStr(in); got != want {
			t.Errorf("intToStr(%d): want %q, got %q", in, want, got)
		}
	}
}
