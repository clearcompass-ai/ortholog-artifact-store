//go:build integration

/*
containers/fakegcs.go — ephemeral fake-gcs-server for GCS conformance.

Image: fsouza/fake-gcs-server (GCS API emulator maintained by the Google
community). Runs the full JSON+XML protocol surface we exercise.

Auth: the fake accepts any bearer token. Tests use a static string; the
backend passes it through via GCSConfig.TokenFunc.

Signed-URL caveat: fake-gcs-server's Resolve path returns working HTTP
URLs but with the fake's own signing (not Google's). Because the artifact
store's Resolve is a passthrough to an injected URLSigner, we inject a
trivial signer in the test suite — see suite_fakegcs_test.go.
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

// FakeGCS bundles the running container with the endpoint and pre-created
// bucket name.
type FakeGCS struct {
	Container testcontainers.Container
	Endpoint  string // e.g., http://localhost:48341
	Bucket    string
}

// StartFakeGCS boots fake-gcs-server with a pre-created bucket.
// The -scheme=http flag matters — the default is TLS with a self-signed
// cert which breaks the artifact store's vanilla http.Client. Production
// uses the real storage.googleapis.com with a real cert; the test fake
// runs plain HTTP and the backend is pointed at it via GCSConfig.BaseURL.
func StartFakeGCS(t *testing.T, ctx context.Context) *FakeGCS {
	t.Helper()

	const bucket = "ortholog-test"

	req := testcontainers.ContainerRequest{
		Image:        imageOrDefault("FAKEGCS_IMAGE", "fsouza/fake-gcs-server:latest"),
		ExposedPorts: []string{"4443/tcp"},
		// -backend memory: no disk persistence (this is a test).
		// -scheme http: avoid TLS.
		// -public-host: required so the server advertises working URLs
		//   back to the client for signed-URL resolution tests.
		Cmd: []string{
			"-backend", "memory",
			"-scheme", "http",
			"-public-host", "localhost",
			"-port", "4443",
		},
		WaitingFor: wait.ForHTTP("/storage/v1/b").
			WithPort("4443/tcp").
			WithStartupTimeout(20 * time.Second),
	}

	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start fake-gcs-server: %v", err)
	}
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = c.Terminate(shutCtx)
	})

	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("fakegcs host: %v", err)
	}
	port, err := c.MappedPort(ctx, "4443/tcp")
	if err != nil {
		t.Fatalf("fakegcs port: %v", err)
	}
	endpoint := fmt.Sprintf("http://%s:%s", host, port.Port())

	fg := &FakeGCS{
		Container: c,
		Endpoint:  endpoint,
		Bucket:    bucket,
	}

	if err := fakegcsCreateBucket(ctx, fg); err != nil {
		t.Fatalf("create bucket on fakegcs: %v", err)
	}
	return fg
}

// fakegcsCreateBucket creates the test bucket via fake-gcs-server's
// admin endpoint. The admin API is a JSON POST /storage/v1/b with a
// {name:...} body. Auth is ignored by the fake.
func fakegcsCreateBucket(ctx context.Context, fg *FakeGCS) error {
	url := fg.Endpoint + "/storage/v1/b"
	body := fmt.Sprintf(`{"name":%q}`, fg.Bucket)
	return doJSONPost(ctx, url, body)
}
