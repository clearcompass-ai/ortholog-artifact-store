//go:build integration

/*
containers/rustfs.go — ephemeral RustFS server for the S3-protocol
conformance suite (Wave 2).

Image: rustfs/rustfs:latest (pin a digest in CI via RUSTFS_IMAGE).
Starts with a fixed root user/pass via RUSTFS_ROOT_USER /
RUSTFS_ROOT_PASSWORD env vars and creates the test bucket during boot
by issuing a SigV4-signed PUT /<bucket>/ over HTTP.

One container is reused across the full scenario matrix in
suite_rustfs_test.go via t.Cleanup at the package level so the ~few-
second startup is amortized.
*/
package containers

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// RustFS bundles a running container with the coordinates tests need
// to point a RustFSBackend at it.
type RustFS struct {
	Container testcontainers.Container
	Endpoint  string // e.g., http://localhost:56321
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
}

// StartRustFS boots a RustFS server on a random host port, creates a
// test bucket, and returns a handle ready to feed into a RustFSBackend.
// Registers t.Cleanup to terminate the container on test completion.
func StartRustFS(t *testing.T, ctx context.Context) *RustFS {
	t.Helper()

	const (
		accessKey = "rustfs-test-root"
		secretKey = "rustfs-test-secret-key-32chars-xx"
		bucket    = "ortholog-test"
		region    = "us-east-1"
	)

	req := testcontainers.ContainerRequest{
		Image:        imageOrDefault("RUSTFS_IMAGE", "rustfs/rustfs:latest"),
		ExposedPorts: []string{"9000/tcp"},
		Env: map[string]string{
			"RUSTFS_ROOT_USER":     accessKey,
			"RUSTFS_ROOT_PASSWORD": secretKey,
		},
		// RustFS exposes the S3 endpoint on :9000. Wait until the listener
		// is accepting TCP connections; the bucket-creation step below
		// then exercises the S3 control plane and acts as the protocol-
		// level readiness probe.
		WaitingFor: wait.ForListeningPort("9000/tcp").
			WithStartupTimeout(45 * time.Second),
	}

	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start RustFS container: %v", err)
	}
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = c.Terminate(shutCtx)
	})

	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("get RustFS host: %v", err)
	}
	port, err := c.MappedPort(ctx, "9000/tcp")
	if err != nil {
		t.Fatalf("get RustFS port: %v", err)
	}
	endpoint := fmt.Sprintf("http://%s:%s", host, port.Port())

	r := &RustFS{
		Container: c,
		Endpoint:  endpoint,
		Region:    region,
		Bucket:    bucket,
		AccessKey: accessKey,
		SecretKey: secretKey,
	}

	// Create the bucket. The TCP listener can come up before the S3
	// control plane is ready to accept PUT /<bucket>/ requests, so we
	// retry a handful of times with a short backoff. Reaching the
	// 200/204 path proves the protocol layer is live.
	if err := createBucketWithRetry(ctx, r); err != nil {
		t.Fatalf("create bucket %q: %v", bucket, err)
	}

	return r
}

// createBucketWithRetry issues PUT /<bucket>/ via SigV4 against the
// running RustFS, retrying briefly on connection-reset / 5xx so a
// just-listening container has time to finish initializing. Uses our
// hand-rolled SigV4 signer so integration tests don't pull aws-sdk-go
// into the module graph.
func createBucketWithRetry(ctx context.Context, r *RustFS) error {
	signer := &SigV4Signer{
		AccessKey: r.AccessKey,
		SecretKey: r.SecretKey,
		Region:    r.Region,
		Service:   "s3",
	}
	url := r.Endpoint + "/" + r.Bucket + "/"

	const attempts = 10
	const backoff = 500 * time.Millisecond
	var lastErr error
	for i := 0; i < attempts; i++ {
		if err := doSignedRequest(ctx, "PUT", url, nil, signer); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
	}
	return errors.Join(fmt.Errorf("create bucket after %d attempts", attempts), lastErr)
}
