package conformance

import (
	"testing"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
)

// Factory produces a fresh BackendProvider for each scenario.
// Scenarios must not share state — a clean backend per scenario keeps
// the suite reliable even when run with t.Parallel().
type Factory func() backends.BackendProvider

// Capabilities describe optional backend features. Some scenarios are
// skipped for backends that do not support them (e.g., IPFS does not
// support Delete).
type Capabilities struct {
	// SupportsDelete is false for IPFS (returns ErrNotSupported).
	SupportsDelete bool
	// SupportsExpiry is true when Resolve returns a non-nil Expiry.
	// IPFS gateway URLs are permanent (Expiry nil); GCS/RustFS presigned
	// URLs have explicit expiry.
	SupportsExpiry bool
	// ExpectedResolveMethod is the storage.Method constant the backend
	// must return from Resolve. Used to catch backends that silently
	// change their method classification.
	ExpectedResolveMethod string
	// SHA256Only is true for backends whose content-addressing pins
	// SHA-256 (currently: IPFS, via the multihash 0x12 tag). Cross-
	// algorithm CIDs registered via storage.RegisterAlgorithm are
	// rejected at the call boundary with storage.ErrNotSupported. The
	// CIDWireForm conformance scenario asserts the SHA-256-only path
	// is honored — silent corruption (stripping the algorithm tag and
	// proceeding) is forbidden by ADR-005 §2.
	SHA256Only bool
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
