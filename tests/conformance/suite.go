package conformance

import (
	"testing"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
)

// Factory produces a fresh BackendProvider for each scenario.
// Scenarios must not share state — a clean backend per scenario keeps
// the suite reliable even when run with t.Parallel().
type Factory func() backends.BackendProvider

// Capabilities describe optional backend features. Scenarios that
// require a feature gate on these flags; backends that don't support
// a feature get the corresponding sub-test skipped or pivoted.
//
// All supported backends (memory, GCS, RustFS, MirroredStore) are
// general-purpose object stores: every algorithm registered through
// storage.RegisterAlgorithm round-trips with CID.Bytes() preserved,
// and Delete is supported. Capabilities reflects the small remaining
// surface where backends genuinely differ: signed-URL TTLs (yes for
// production; no for the in-memory reference) and the resolve method
// vocabulary.
type Capabilities struct {
	// SupportsDelete must be true for any object-store backend. The
	// flag exists for forward compatibility with backend kinds that
	// might be append-only (none today). All four supported backends
	// pass it as true.
	SupportsDelete bool

	// SupportsExpiry is true when Resolve returns a non-nil Expiry.
	// GCS / RustFS produce signed URLs with an explicit TTL; the
	// in-memory backend returns MethodDirect with nil Expiry.
	SupportsExpiry bool

	// ExpectedResolveMethod is the storage.Method constant the
	// backend must return from Resolve. Used to catch backends that
	// silently change their method classification.
	ExpectedResolveMethod string
}

// RunBackendConformance runs every scenario category against the backend
// produced by factory. name is used in t.Run subtests for identification.
func RunBackendConformance(t *testing.T, name string, factory Factory, caps Capabilities) {
	t.Helper()

	t.Run(name+"/Lifecycle", func(t *testing.T) { runLifecycle(t, factory, caps) })
	t.Run(name+"/Resolve", func(t *testing.T) { runResolve(t, factory, caps) })
	t.Run(name+"/Errors", func(t *testing.T) { runErrors(t, factory, caps) })
	t.Run(name+"/Concurrent", func(t *testing.T) { runConcurrent(t, factory, caps) })
	t.Run(name+"/Integrity", func(t *testing.T) { runIntegrity(t, factory, caps) })
	t.Run(name+"/CIDWireForm", func(t *testing.T) { runCIDWireForm(t, factory, caps) })
}
