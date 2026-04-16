//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
)

// TestMain is the Wave 2 entry point. Its only job is to establish that
// Docker is reachable — if not, fail loudly rather than silently skip.
// Per-suite tests bring up their own containers via helpers in ./containers/.
func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// testcontainers-go's provider resolution hits the Docker socket.
	// If the daemon is down, the socket is missing, or permissions are
	// wrong, this errors immediately instead of hanging.
	provider, err := testcontainers.NewDockerProvider()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: integration tests require Docker; NewDockerProvider: %v\n", err)
		fmt.Fprintf(os.Stderr, "       Run: `docker info` to diagnose.\n")
		fmt.Fprintf(os.Stderr, "       To skip integration tests, omit -tags=integration.\n")
		os.Exit(2)
	}
	defer provider.Close()

	if err := provider.Health(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: Docker daemon unhealthy: %v\n", err)
		os.Exit(2)
	}

	os.Exit(m.Run())
}
