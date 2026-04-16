//go:build integration

/*
containers/minio.go — ephemeral MinIO server for S3 conformance tests.

Image: minio/minio:latest (pinned to a digest in CI via image override env).
Starts with a fixed root user/pass and creates the test bucket during
boot via the mc client's post-exec hook. Returns an endpoint plus the
credentials the S3Backend needs to speak to it.

One container per Go subtest is wasteful — startup costs ~8s. Tests in
suite_minio_test.go reuse a single container across the full scenario
matrix via t.Cleanup registration at the package level.
*/
package containers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// MinIO bundles a running container with the coordinates tests need
// to point an S3Backend at it.
type MinIO struct {
	Container testcontainers.Container
	Endpoint  string // e.g., http://localhost:56321
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
}

// StartMinIO boots a MinIO server on a random host port, creates a test
// bucket, and returns a handle ready to feed into an S3Backend.
// Registers t.Cleanup to terminate the container on test completion.
func StartMinIO(t *testing.T, ctx context.Context) *MinIO {
	t.Helper()

	const (
		accessKey = "minio-test-root"
		secretKey = "minio-test-secret-key-32chars-xxx"
		bucket    = "ortholog-test"
		region    = "us-east-1"
	)

	req := testcontainers.ContainerRequest{
		Image:        imageOrDefault("MINIO_IMAGE", "minio/minio:latest"),
		ExposedPorts: []string{"9000/tcp"},
		Env: map[string]string{
			"MINIO_ROOT_USER":     accessKey,
			"MINIO_ROOT_PASSWORD": secretKey,
		},
		Cmd: []string{"server", "/data", "--address", ":9000"},
		// Wait for the HTTP health endpoint to come up. `minio/health/ready`
		// flips to 200 only after the erasure-coded volumes are healthy.
		WaitingFor: wait.ForHTTP("/minio/health/ready").
			WithPort("9000/tcp").
			WithStartupTimeout(30 * time.Second),
	}

	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start MinIO container: %v", err)
	}
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = c.Terminate(shutCtx)
	})

	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("get MinIO host: %v", err)
	}
	port, err := c.MappedPort(ctx, "9000/tcp")
	if err != nil {
		t.Fatalf("get MinIO port: %v", err)
	}
	endpoint := fmt.Sprintf("http://%s:%s", host, port.Port())

	m := &MinIO{
		Container: c,
		Endpoint:  endpoint,
		Region:    region,
		Bucket:    bucket,
		AccessKey: accessKey,
		SecretKey: secretKey,
	}

	// Create the bucket. Using MinIO's own `mc` as a sidecar would double
	// container count; instead we call the S3 API directly via a single
	// PUT /<bucket>/ request signed with the root credentials.
	if err := createBucket(ctx, m); err != nil {
		t.Fatalf("create bucket %q: %v", bucket, err)
	}

	return m
}

// createBucket issues a bucket PUT against the running MinIO. Uses a
// hand-rolled SigV4-style signer living in sigv4.go (same package) so
// integration tests do NOT depend on aws-sdk-go-v2 at module level.
func createBucket(ctx context.Context, m *MinIO) error {
	signer := &SigV4Signer{
		AccessKey: m.AccessKey,
		SecretKey: m.SecretKey,
		Region:    m.Region,
		Service:   "s3",
	}
	url := m.Endpoint + "/" + m.Bucket + "/"
	return doSignedRequest(ctx, "PUT", url, nil, signer)
}
