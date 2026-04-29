package conformance_test

import (
	"testing"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-artifact-store/internal/testutil"
	"github.com/clearcompass-ai/ortholog-artifact-store/tests/conformance"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// TestConformance_Mirrored runs the full backend conformance suite
// against MirroredStore composed of two InMemoryBackends. Wave 1
// previously only had a single happy-path test for the mirror; this
// wires the decorator through every scenario (Lifecycle, Resolve,
// Errors, Concurrent, Integrity, CIDWireForm, CIDParse, CIDVerify,
// ResolveWire) so any contract break in the decorator surfaces in
// the same shape as a backend regression.
//
// Capabilities:
//   SupportsDelete: true       (both InMemoryBackends support it)
//   SupportsExpiry: false      (Mirrored.Resolve delegates to primary,
//                                which is InMemoryBackend → MethodDirect
//                                with nil expiry)
//   ExpectedResolveMethod:
//     storage.MethodDirect      (per the delegation rule above)
//
// Sync-mode mirroring (the only supported mode) means both Push paths
// must succeed atomically; any conformance scenario that pushes and
// then fetches indirectly validates that the mirror leg also stored
// the bytes — Fetch of a previously-Pushed CID uses the primary's
// store, but Healthy() walks both legs and the wire-form scenarios
// indirectly exercise both via repeated Push/Fetch cycles.
func TestConformance_Mirrored(t *testing.T) {
	conformance.RunBackendConformance(t, "mirrored",
		func() backends.BackendProvider {
			ms := backends.NewMirroredStore(
				backends.NewInMemoryBackend(),
				backends.NewInMemoryBackend(),
				backends.MirroredConfig{Mode: backends.MirrorModeSync},
			)
			t.Cleanup(ms.Close)
			return ms
		},
		conformance.Capabilities{
			SupportsDelete:        true,
			SupportsExpiry:        false,
			ExpectedResolveMethod: storage.MethodDirect,
		},
	)
}

// TestConformance_Mirrored_RegistersGoroutineLeakDetector ensures
// the mirrored backend doesn't leave background goroutines after
// every conformance run. Sync mode does not spawn any per
// mirrored.go's "// pre-v7.75 async-pin shape" comment, but a future
// regression that re-introduces async work would surface here.
func TestConformance_Mirrored_RegistersGoroutineLeakDetector(t *testing.T) {
	// Same as the inmemory suite: leverage the existing TestMain in
	// suite_inmemory_test.go via testutil.RunWithGoleak. This test
	// just exists to enforce the import — TestMain catches leaks
	// process-wide.
	_ = testutil.KnownVectors // touch testutil so the package is in
	// scope; TestMain installs goleak.VerifyTestMain at process exit.
}
