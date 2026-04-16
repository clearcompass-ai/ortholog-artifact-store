//go:build staging

package staging

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-artifact-store/internal/signers"
	"github.com/clearcompass-ai/ortholog-artifact-store/tests/conformance"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// newAWSS3Backend constructs an S3Backend pointed at real AWS, using
// real SigV4 for request signing and presigning.
func newAWSS3Backend(t *testing.T, prefix string) backends.BackendProvider {
	t.Helper()
	if !awsConfigured() {
		t.Skipf("AWS credentials not configured")
	}
	region := os.Getenv("STAGING_AWS_REGION")
	endpoint := fmt.Sprintf("https://s3.%s.amazonaws.com", region)

	sigv4 := &signers.SigV4{
		AccessKey: os.Getenv("STAGING_AWS_ACCESS_KEY_ID"),
		SecretKey: os.Getenv("STAGING_AWS_SECRET_ACCESS_KEY"),
		Region:    region,
		Service:   "s3",
	}
	return backends.NewS3Backend(backends.S3Config{
		Endpoint:      endpoint,
		Bucket:        os.Getenv("STAGING_AWS_BUCKET"),
		Region:        region,
		Prefix:        prefix,
		PathStyle:     false, // AWS: virtual-host style is production default
		RequestSigner: sigv4,
		URLSigner:     &signers.BoundS3Presigner{Signer: sigv4, Endpoint: endpoint},
	})
}

// TestConformance_AWS_S3 runs the full conformance suite against a
// real AWS S3 bucket. This is the layer that catches IAM edge cases,
// SigV4 clock skew, regional redirects, and virtual-host-style routing.
func TestConformance_AWS_S3(t *testing.T) {
	if !awsConfigured() {
		t.Skip("AWS not configured for this run")
	}
	recordOp(t)

	prefix := randomPrefix(t)
	factory := func() backends.BackendProvider {
		return newAWSS3Backend(t, prefix)
	}

	conformance.RunBackendConformance(t, "aws-s3", factory,
		conformance.Capabilities{
			SupportsDelete:        true,
			SupportsExpiry:        true,
			ExpectedResolveMethod: storage.MethodSignedURL,
		})
}

// TestAWS_S3_Healthy localizes setup failures: credentials wrong, bucket
// wrong region, bucket doesn't exist. Runs before the big suite so a
// credential-level failure shows up cleanly at the top of the log.
func TestAWS_S3_Healthy(t *testing.T) {
	if !awsConfigured() {
		t.Skip("AWS not configured")
	}
	b := newAWSS3Backend(t, randomPrefix(t))
	recordOp(t)
	if err := b.Healthy(); err != nil {
		t.Fatalf("AWS S3 Healthy: %v (check STAGING_AWS_* env + bucket region)", err)
	}
}

// TestAWS_S3_PresignedURLIsFetchable is Wave 3's unique value: it proves
// that our SigV4 presigner produces URLs that AWS actually accepts.
// Wave 2 (MinIO) tests URL shape; Wave 3 tests URL validity. Without
// this, a subtle canonicalization bug would only surface in production.
func TestAWS_S3_PresignedURLIsFetchable(t *testing.T) {
	if !awsConfigured() {
		t.Skip("AWS not configured")
	}
	prefix := randomPrefix(t)
	b := newAWSS3Backend(t, prefix)

	data := []byte("staging-presigned-fetch")
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
	if cred.Method != storage.MethodSignedURL {
		t.Fatalf("Method: want %s, got %s", storage.MethodSignedURL, cred.Method)
	}

	// Actually GET the URL with a plain HTTP client — no auth headers.
	// If AWS returns 200 + the right bytes, SigV4 presigning works.
	got, err := fetchURLBytes(context.Background(), cred.URL)
	if err != nil {
		t.Fatalf("GET presigned URL: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("presigned URL returned wrong bytes:\n  want=%x\n  got =%x", data, got)
	}
}

// TestAWS_S3_404NotFound locks AWS's actual 404 behavior for missing
// objects vs our backend's mapping to storage.ErrContentNotFound.
func TestAWS_S3_404NotFound(t *testing.T) {
	if !awsConfigured() {
		t.Skip("AWS not configured")
	}
	b := newAWSS3Backend(t, randomPrefix(t))

	cid := storage.Compute([]byte("never-was-pushed"))
	_, err := b.Fetch(cid)
	recordOp(t)
	if !errors.Is(err, storage.ErrContentNotFound) {
		t.Fatalf("Fetch missing: want ErrContentNotFound, got %v", err)
	}
}
