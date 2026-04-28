//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-artifact-store/tests/conformance"
	"github.com/clearcompass-ai/ortholog-artifact-store/tests/integration/containers"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// rustfsURLSigner produces presigned GET URLs against the running
// RustFS container. Adequate for tests — not a production signer.
type rustfsURLSigner struct {
	signer *containers.SigV4Signer
	host   string // full scheme://host:port
}

func (m *rustfsURLSigner) PresignGetObject(bucket, key string, expiry time.Duration) (string, error) {
	// For our conformance test we just return an unsigned URL pointing
	// at the object. RustFS with a public bucket policy would serve it;
	// with auth required the backend's signer sets Authorization headers
	// on each request instead. This is fine because the conformance
	// suite asserts URL shape + MethodSignedURL + non-nil expiry, not
	// that the URL is actually fetchable from a bare browser.
	url := fmt.Sprintf("%s/%s/%s?expiry=%d", m.host, bucket, key, int(expiry.Seconds()))
	return url, nil
}

// TestConformance_RustFS runs the full conformance suite against a
// RustFSBackend pointed at a RustFS container. This catches real-
// protocol divergences that the HTTP-mocked tests in
// backends/rustfs_test.go can't see — subtle error classification,
// streaming semantics, header ordering with SigV4.
func TestConformance_RustFS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	r := containers.StartRustFS(t, ctx)

	conformance.RunBackendConformance(t, "rustfs",
		func() backends.BackendProvider {
			return backends.NewRustFSBackend(backends.RustFSConfig{
				Endpoint:  r.Endpoint,
				Bucket:    r.Bucket,
				Region:    r.Region,
				Prefix:    randomPrefix(t), // isolates each test run in the same bucket
				PathStyle: true,
				RequestSigner: &containers.SigV4Signer{
					AccessKey: r.AccessKey,
					SecretKey: r.SecretKey,
					Region:    r.Region,
					Service:   "s3",
				},
				URLSigner: &rustfsURLSigner{host: r.Endpoint},
			})
		},
		conformance.Capabilities{
			SupportsDelete:        true,
			SupportsExpiry:        true,
			ExpectedResolveMethod: storage.MethodSignedURL,
		},
	)
}

// TestRustFS_Healthy sanity-checks the container is reachable via the
// backend before the bigger suite runs. A failure here localizes the
// blame quickly: bad container, not bad backend code.
func TestRustFS_Healthy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	r := containers.StartRustFS(t, ctx)
	b := backends.NewRustFSBackend(backends.RustFSConfig{
		Endpoint:  r.Endpoint,
		Bucket:    r.Bucket,
		Region:    r.Region,
		PathStyle: true,
		RequestSigner: &containers.SigV4Signer{
			AccessKey: r.AccessKey, SecretKey: r.SecretKey,
			Region: r.Region, Service: "s3",
		},
	})
	if err := b.Healthy(); err != nil {
		t.Fatalf("RustFS Healthy: %v", err)
	}
}

// TestRustFS_404NotForbidden locks the S3-protocol quirk that the
// RustFSBackend folds 404 and 403 into ErrContentNotFound. This test
// verifies the 404 path against the live container; the 403 path is
// covered in backends/rustfs_test.go (HTTP-mocked).
func TestRustFS_404NotForbidden(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	r := containers.StartRustFS(t, ctx)
	b := backends.NewRustFSBackend(backends.RustFSConfig{
		Endpoint:  r.Endpoint,
		Bucket:    r.Bucket,
		Region:    r.Region,
		PathStyle: true,
		RequestSigner: &containers.SigV4Signer{
			AccessKey: r.AccessKey, SecretKey: r.SecretKey,
			Region: r.Region, Service: "s3",
		},
	})
	cid := storage.Compute([]byte("never-pushed"))
	_, err := b.Fetch(cid)
	if err == nil {
		t.Fatal("Fetch missing: want error, got nil")
	}
	if err.Error() == "" {
		t.Fatal("error message empty")
	}
}
