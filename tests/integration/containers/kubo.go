//go:build integration

/*
containers/kubo.go — ephemeral Kubo (go-ipfs) for IPFS conformance.

Image: ipfs/kubo:latest (pinned via env in CI).
Exposes the Kubo RPC API on :5001 and the HTTP gateway on :8080.
Initialized with the `test` profile to avoid running a real DHT.

The Kubo container does not require auth; real Filebase deployments do.
The Wave 3 staging suite exercises the bearer-token path (see backends/ipfs_test.go
for the HTTP-mocked analogue, and tests/staging/ for real Filebase).
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

// Kubo bundles the container with the RPC API endpoint and gateway URL.
type Kubo struct {
	Container   testcontainers.Container
	APIEndpoint string // e.g., http://localhost:54873
	Gateway     string // e.g., http://localhost:54874
}

// StartKubo boots a Kubo daemon initialized for testing.
//
// IPFS_PROFILE=test disables the DHT and external bootstrap, so the
// container stays fully isolated — no outbound traffic, no surprise
// content from the public network.
func StartKubo(t *testing.T, ctx context.Context) *Kubo {
	t.Helper()

	req := testcontainers.ContainerRequest{
		Image:        imageOrDefault("KUBO_IMAGE", "ipfs/kubo:latest"),
		ExposedPorts: []string{"5001/tcp", "8080/tcp"},
		Env: map[string]string{
			"IPFS_PROFILE": "test",
		},
		// Kubo emits "Daemon is ready" on stdout once the RPC API is live.
		// That's more reliable than an HTTP probe because /api/v0/id
		// requires a POST (HTTP probes default to GET).
		WaitingFor: wait.ForLog("Daemon is ready").
			WithStartupTimeout(60 * time.Second),
	}

	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start kubo container: %v", err)
	}
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = c.Terminate(shutCtx)
	})

	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("kubo host: %v", err)
	}
	apiPort, err := c.MappedPort(ctx, "5001/tcp")
	if err != nil {
		t.Fatalf("kubo api port: %v", err)
	}
	gwPort, err := c.MappedPort(ctx, "8080/tcp")
	if err != nil {
		t.Fatalf("kubo gateway port: %v", err)
	}

	return &Kubo{
		Container:   c,
		APIEndpoint: fmt.Sprintf("http://%s:%s", host, apiPort.Port()),
		Gateway:     fmt.Sprintf("http://%s:%s", host, gwPort.Port()),
	}
}
