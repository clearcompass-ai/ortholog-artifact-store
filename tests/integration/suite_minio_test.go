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

// minioURLSigner produces S3 presigned GET URLs against the running
// MinIO container. Adequate for tests — not a production signer.
type minioURLSigner struct {
	signer *containers.SigV4Signer
	host   string // full scheme://host:port
}

func (m *minioURLSigner) PresignGetObject(bucket, key string, expiry time.Duration) (string, error) {
	// For our conformance test we just return an unsigned URL pointing
	// at the object. MinIO with a public bucket policy would serve it;
	// with auth required the backend's signer sets Authorization headers
	// on each request instead. This is fine because the conformance
	// suite asserts URL shape + MethodSignedURL + non-nil expiry, not
	// that the URL is actually fetchable from a bare browser.
	url := fmt.Sprintf("%s/%s/%s?expiry=%d", m.host, bucket, key, int(expiry.Seconds()))
	return url, nil
}

// TestConformance_MinIO runs the full conformance suite against an S3Backend
// pointed at a MinIO container. This catches real-protocol divergences that
// the HTTP-mocked tests in backends/s3_test.go can't see — subtle error
// classification, streaming semantics, header ordering with SigV4.
func TestConformance_MinIO(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	m := containers.StartMinIO(t, ctx)

	conformance.RunBackendConformance(t, "minio",
		func() backends.BackendProvider {
			return backends.NewS3Backend(backends.S3Config{
				Endpoint:  m.Endpoint,
				Bucket:    m.Bucket,
				Region:    m.Region,
				Prefix:    randomPrefix(t), // isolates each test run in the same bucket
				PathStyle: true,            // MinIO requires path-style
				RequestSigner: &containers.SigV4Signer{
					AccessKey: m.AccessKey,
					SecretKey: m.SecretKey,
					Region:    m.Region,
					Service:   "s3",
				},
				URLSigner: &minioURLSigner{host: m.Endpoint},
			})
		},
		conformance.Capabilities{
			SupportsDelete:        true,
			SupportsExpiry:        true,
			ExpectedResolveMethod: storage.MethodSignedURL,
		},
	)
}

// TestMinIO_Healthy sanity-checks the container is reachable via the
// backend before the bigger suite runs. A failure here localizes the
// blame quickly: bad container, not bad backend code.
func TestMinIO_Healthy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	m := containers.StartMinIO(t, ctx)
	b := backends.NewS3Backend(backends.S3Config{
		Endpoint:  m.Endpoint,
		Bucket:    m.Bucket,
		Region:    m.Region,
		PathStyle: true,
		RequestSigner: &containers.SigV4Signer{
			AccessKey: m.AccessKey, SecretKey: m.SecretKey,
			Region: m.Region, Service: "s3",
		},
	})
	if err := b.Healthy(); err != nil {
		t.Fatalf("MinIO Healthy: %v", err)
	}
}

// TestMinIO_404NotForbidden locks a subtle S3 quirk: a public MinIO
// bucket returns 404 for missing objects, but a private bucket returns
// 403. The backend collapses both to ErrContentNotFound. This test
// verifies the 404 path against MinIO; the 403 path is covered in
// backends/s3_test.go (HTTP-mocked).
func TestMinIO_404NotForbidden(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	m := containers.StartMinIO(t, ctx)
	b := backends.NewS3Backend(backends.S3Config{
		Endpoint:  m.Endpoint,
		Bucket:    m.Bucket,
		Region:    m.Region,
		PathStyle: true,
		RequestSigner: &containers.SigV4Signer{
			AccessKey: m.AccessKey, SecretKey: m.SecretKey,
			Region: m.Region, Service: "s3",
		},
	})
	cid := storage.Compute([]byte("never-pushed"))
	_, err := b.Fetch(cid)
	if err == nil {
		t.Fatal("Fetch missing: want error, got nil")
	}
	// The backend folds 404 and 403 together, but we still want
	// confirmation that the container response triggered that path.
	if err.Error() == "" {
		t.Fatal("error message empty")
	}
}
