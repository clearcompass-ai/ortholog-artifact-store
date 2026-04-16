package conformance_test

import (
	"testing"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-artifact-store/internal/testutil"
	"github.com/clearcompass-ai/ortholog-artifact-store/tests/conformance"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// TestMain enforces goroutine leak detection for the conformance suite.
// Tests that launch background workers (conceptually, MirroredStore
// conformance runs will) must clean up before the test binary exits.
func TestMain(m *testing.M) {
	testutil.RunWithGoleak(m)
}

// TestConformance_InMemory runs the full conformance suite against the
// InMemoryBackend. This is the first consumer of the suite and validates
// that the suite itself is correct before we apply it to real backends.
func TestConformance_InMemory(t *testing.T) {
	conformance.RunBackendConformance(t, "inmemory",
		func() backends.BackendProvider {
			return backends.NewInMemoryBackend()
		},
		conformance.Capabilities{
			SupportsDelete:        true,
			SupportsExpiry:        false, // InMemoryBackend returns MethodDirect with nil expiry
			ExpectedResolveMethod: storage.MethodDirect,
		},
	)
}
