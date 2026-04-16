//go:build staging

package staging

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-artifact-store/internal/signers"
	"github.com/clearcompass-ai/ortholog-artifact-store/tests/conformance"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// gcsTokenSource is a minimal OAuth2 client_credentials exchange.
// We avoid pulling in golang.org/x/oauth2 because it has a large
// transitive dependency surface and Wave 3 test code shouldn't
// leak into Wave 1's go.sum.
//
// For staging tests we use the metadata-server path when available
// (GKE, GCE, Cloud Run) OR a service-account JSON key via the same
// package that produces signed URLs. This file implements the JWT
// exchange path for local CI runners using a service-account key.
type gcsTokenSource struct {
	jsonPath string

	mu          sync.Mutex
	cachedToken string
	expiresAt   time.Time
}

func (g *gcsTokenSource) Token() (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if time.Now().Before(g.expiresAt.Add(-60 * time.Second)) {
		return g.cachedToken, nil
	}

	// Exchange the service-account JWT for an access token. The staging
	// suite uses the gcloud CLI if available as a backstop — but the
	// expected path is to set GOOGLE_APPLICATION_CREDENTIALS and let
	// the ADC-equivalent work. For simplicity here we shell out via
	// the signers package's ability to produce a JWT and let the
	// oauth2 endpoint exchange it.
	//
	// (Simplified: in CI, inject the token directly via an env var so
	// the test code does no network I/O to auth.)
	if t := os.Getenv("STAGING_GCS_ACCESS_TOKEN"); t != "" {
		g.cachedToken = t
		g.expiresAt = time.Now().Add(50 * time.Minute)
		return t, nil
	}
	return "", fmt.Errorf("no STAGING_GCS_ACCESS_TOKEN provided; set it from a short-lived gcloud token")
}

// newGCSBackend constructs a GCSBackend pointed at real GCS using
// a service-account URL signer and a short-lived access token.
func newGCSBackend(t *testing.T, prefix string) backends.BackendProvider {
	t.Helper()
	if !gcsConfigured() {
		t.Skipf("GCS credentials not configured")
	}
	signer, err := signers.LoadGCSServiceAccount(os.Getenv("STAGING_GCS_SERVICE_ACCOUNT_JSON"))
	if err != nil {
		t.Fatalf("load GCS service account: %v", err)
	}
	tok := &gcsTokenSource{jsonPath: os.Getenv("STAGING_GCS_SERVICE_ACCOUNT_JSON")}

	return backends.NewGCSBackend(backends.GCSConfig{
		Bucket:    os.Getenv("STAGING_GCS_BUCKET"),
		Prefix:    prefix,
		TokenFunc: tok.Token,
		URLSigner: signer,
	})
}

// TestConformance_GCS runs the full conformance suite against real GCS.
func TestConformance_GCS(t *testing.T) {
	if !gcsConfigured() {
		t.Skip("GCS not configured for this run")
	}
	recordOp(t)
	prefix := randomPrefix(t)
	factory := func() backends.BackendProvider {
		return newGCSBackend(t, prefix)
	}

	conformance.RunBackendConformance(t, "gcs", factory,
		conformance.Capabilities{
			SupportsDelete:        true,
			SupportsExpiry:        true,
			ExpectedResolveMethod: storage.MethodSignedURL,
		})
}

// TestGCS_Healthy localizes credential failures at the top of the log.
func TestGCS_Healthy(t *testing.T) {
	if !gcsConfigured() {
		t.Skip("GCS not configured")
	}
	b := newGCSBackend(t, randomPrefix(t))
	recordOp(t)
	if err := b.Healthy(); err != nil {
		t.Fatalf("GCS Healthy: %v", err)
	}
}

// TestGCS_SignedURLIsFetchable proves the V4 signing produces a URL
// that real GCS accepts. This is the entire reason we maintain a
// signers.GCSSigner implementation instead of just a mock.
func TestGCS_SignedURLIsFetchable(t *testing.T) {
	if !gcsConfigured() {
		t.Skip("GCS not configured")
	}
	b := newGCSBackend(t, randomPrefix(t))

	data := []byte("staging-gcs-signed-url")
	cid := storage.Compute(data)
	if err := b.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}
	recordOp(t)

	cred, err := b.Resolve(cid, 120*time.Second)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	recordOp(t)

	got, err := fetchURLBytes(context.Background(), cred.URL)
	if err != nil {
		t.Fatalf("GET signed URL: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("signed URL returned wrong bytes")
	}
}

// TestGCS_404NotFound validates GCS's real 404 behavior maps to
// storage.ErrContentNotFound.
func TestGCS_404NotFound(t *testing.T) {
	if !gcsConfigured() {
		t.Skip("GCS not configured")
	}
	b := newGCSBackend(t, randomPrefix(t))
	cid := storage.Compute([]byte("never-pushed"))
	_, err := b.Fetch(cid)
	recordOp(t)
	if !errors.Is(err, storage.ErrContentNotFound) {
		t.Fatalf("Fetch missing: want ErrContentNotFound, got %v", err)
	}
}

// Unused placeholder keeps the import list honest even if a future
// test drops direct http usage.
var _ = http.MethodGet
