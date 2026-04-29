package conformance_test

import (
	"testing"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-artifact-store/internal/testutil"
	"github.com/clearcompass-ai/ortholog-artifact-store/tests/conformance"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// TestConformance_NotSupported runs the full conformance suite against
// a backend whose Delete returns storage.ErrNotSupported. This is the
// only consumer in the test matrix that exercises the
// SupportsDelete=false branch — every production backend declares
// SupportsDelete=true, leaving the dead branch in scenarios_lifecycle.go
// without coverage.
//
// The branch matters: lifecycle.GrantArtifactAccess (SDK side) treats
// storage.ErrNotSupported as a recoverable signal that an artifact
// won't be cryptographically erased on revocation, and chooses an
// alternate cleanup path. If a backend silently returns nil instead
// of the SDK sentinel, the lifecycle layer never knows the deletion
// didn't happen — that regression is caught here.
//
// Coverage:
//   - delete_returns_not_supported (the gated scenario)
//   - everything else in the matrix (Push/Fetch/Pin/Exists/Resolve/...)
//     still passes — the synthetic backend embeds InMemoryContentStore
//     for those methods.
func TestConformance_NotSupported(t *testing.T) {
	conformance.RunBackendConformance(t, "notsupported",
		func() backends.BackendProvider {
			// NotSupportedBackend satisfies BackendProvider via
			// structural typing — it has the right method shapes
			// (storage.ContentStore + Resolve + Healthy).
			return testutil.NewNotSupportedBackend()
		},
		conformance.Capabilities{
			SupportsDelete:        false, // exercises the gated branch
			SupportsExpiry:        false,
			ExpectedResolveMethod: storage.MethodDirect,
		},
	)
}
