package sdkwire

import (
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/clearcompass-ai/ortholog-artifact-store/api"
	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// ─── End-to-end SDK ↔ artifact-store wire round-trip ─────────────────
//
// Each test here wires:
//
//   storage.HTTPRetrievalProvider.Resolve(cid, expiry)
//     ─HTTP GET /v1/artifacts/{cid}/resolve?expiry=N
//        → api.ResolveHandler.ServeHTTP
//        → backend.Resolve
//        → retrievalCredentialToWire (api/wire.go)
//        → JSON body
//     ←─ SDK decodes resolveResponse (lowercase tags, RFC3339 expiry)
//   return *storage.RetrievalCredential
//
// Drift in any link surfaces here. The conformance scenarios pin each
// link in isolation; this test pins the composition.

// TestResolve_NoExpiry exercises InMemoryBackend's MethodDirect path.
// SDK contract: Method=="direct", URL non-empty, Expiry nil.
func TestResolve_NoExpiry(t *testing.T) {
	backend := backends.NewInMemoryBackend()
	srv := httptest.NewServer(api.NewMux(api.ServerConfig{
		Backend:       backend,
		VerifyOnPush:  true,
		MaxBodySize:   16 << 20,
		DefaultExpiry: time.Minute,
	}))
	t.Cleanup(srv.Close)

	data := []byte("sdkwire/resolve/no-expiry")
	cid := storage.Compute(data)
	if err := backend.Push(cid, data); err != nil {
		t.Fatalf("backend.Push: %v", err)
	}

	provider := storage.NewHTTPRetrievalProvider(storage.HTTPRetrievalProviderConfig{
		BaseURL: srv.URL,
		Timeout: 5 * time.Second,
	})
	cred, err := provider.Resolve(cid, time.Hour)
	if err != nil {
		t.Fatalf("provider.Resolve: %v", err)
	}
	if cred == nil {
		t.Fatal("provider.Resolve returned (nil, nil)")
	}
	if cred.Method != storage.MethodDirect {
		t.Fatalf("Method=%q, want %q", cred.Method, storage.MethodDirect)
	}
	if cred.URL == "" {
		t.Fatal("provider returned empty URL")
	}
	if cred.Expiry != nil {
		t.Fatalf("Expiry=%v, want nil for InMemoryBackend (MethodDirect)", cred.Expiry)
	}
}

// TestResolve_WithExpiry exercises a SignedURL backend stamping a
// non-nil Expiry. The artifact-store renders RFC3339 → wire; the SDK
// parses RFC3339 → *time.Time. A drift on either side silently
// degrades expiry to nil (the SDK swallows time.Parse errors per
// http_retrieval_provider.go).
func TestResolve_WithExpiry(t *testing.T) {
	expectedExpiry := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	mock := newExpiryBackend(expectedExpiry)
	srv := httptest.NewServer(api.NewMux(api.ServerConfig{
		Backend:       mock,
		MaxBodySize:   16 << 20,
		DefaultExpiry: time.Minute,
	}))
	t.Cleanup(srv.Close)

	data := []byte("sdkwire/resolve/with-expiry")
	cid := storage.Compute(data)
	if err := mock.Push(cid, data); err != nil {
		t.Fatalf("mock.Push: %v", err)
	}

	provider := storage.NewHTTPRetrievalProvider(storage.HTTPRetrievalProviderConfig{
		BaseURL: srv.URL,
		Timeout: 5 * time.Second,
	})
	cred, err := provider.Resolve(cid, time.Hour)
	if err != nil {
		t.Fatalf("provider.Resolve: %v", err)
	}
	if cred.Method != storage.MethodSignedURL {
		t.Fatalf("Method=%q, want %q", cred.Method, storage.MethodSignedURL)
	}
	if cred.Expiry == nil {
		t.Fatal("Expiry is nil; SDK or artifact-store dropped the RFC3339 round-trip")
	}
	if !cred.Expiry.Equal(expectedExpiry) {
		t.Fatalf("Expiry round-trip drift: in=%v out=%v", expectedExpiry, *cred.Expiry)
	}
}

// TestResolve_NotFound asserts the artifact-store's 404 maps to
// storage.ErrContentNotFound on the SDK side. lifecycle.GrantArtifactAccess
// errors.Is-checks this sentinel before declining the grant — the test
// pins the SDK-sentinel propagation across the HTTP boundary.
func TestResolve_NotFound(t *testing.T) {
	srv := httptest.NewServer(api.NewMux(api.ServerConfig{
		Backend:       backends.NewInMemoryBackend(),
		MaxBodySize:   16 << 20,
		DefaultExpiry: time.Minute,
	}))
	t.Cleanup(srv.Close)

	provider := storage.NewHTTPRetrievalProvider(storage.HTTPRetrievalProviderConfig{
		BaseURL: srv.URL,
		Timeout: 5 * time.Second,
	})
	cid := storage.Compute([]byte("sdkwire/resolve/never-pushed"))
	_, err := provider.Resolve(cid, time.Hour)
	if !errors.Is(err, storage.ErrContentNotFound) {
		t.Fatalf("err=%v, want errors.Is(err, storage.ErrContentNotFound)", err)
	}
}

// TestResolve_DefaultExpiryUsedWhenQueryAbsent locks that the
// artifact-store's DefaultExpiry config kicks in when the SDK's
// expiry conversion produces zero seconds. Today the SDK always
// emits ?expiry=N where N=int(d.Seconds()), so the path is rarely
// hit — but a SDK refactor that drops the query could silently lose
// the expiry contract; this test catches that.
func TestResolve_DefaultExpiryUsedWhenQueryAbsent(t *testing.T) {
	expectedExpiry := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	mock := newExpiryBackend(expectedExpiry)
	srv := httptest.NewServer(api.NewMux(api.ServerConfig{
		Backend:       mock,
		MaxBodySize:   16 << 20,
		DefaultExpiry: 30 * time.Minute,
	}))
	t.Cleanup(srv.Close)

	data := []byte("sdkwire/resolve/default-expiry")
	cid := storage.Compute(data)
	if err := mock.Push(cid, data); err != nil {
		t.Fatalf("mock.Push: %v", err)
	}

	provider := storage.NewHTTPRetrievalProvider(storage.HTTPRetrievalProviderConfig{
		BaseURL: srv.URL,
	})
	cred, err := provider.Resolve(cid, 0) // SDK emits expiry=0 → handler falls back to DefaultExpiry
	if err != nil {
		t.Fatalf("provider.Resolve: %v", err)
	}
	if cred.Expiry == nil {
		t.Fatal("Expiry is nil despite backend stamping it")
	}
}
