// Package conformance is the backend contract test suite.
//
// Every implementation of backends.BackendProvider — InMemoryBackend,
// GCSBackend, RustFSBackend, IPFSBackend, MirroredStore — is expected
// to pass this suite. The suite is the single source of truth for
// backend behavior.
//
// Consumers register a factory and get the full matrix of tests:
//
//	func TestInMemory(t *testing.T) {
//	    conformance.RunBackendConformance(t, "inmemory", func() backends.BackendProvider {
//	        return backends.NewInMemoryBackend()
//	    })
//	}
//
// The factory is called fresh per scenario — scenarios must not leak
// state between each other. The suite itself lives outside _test.go
// files so it can be imported from multiple test packages (unit tests
// here, integration tests against containerized RustFS / Kubo /
// fake-gcs-server in Wave 2, staging tests against real GCS and
// Filebase IPFS in Wave 3).
//
// What the suite covers:
//   - Lifecycle: push → fetch → exists → delete round-trips
//   - Resolve: correct Method constants, expiry semantics, not-found
//   - Errors: ErrContentNotFound, ErrNotSupported classification
//   - Concurrent: parallel push/fetch produce coherent state
//   - Integrity: byte-identical round-trip for small, medium, and
//     exact-power-of-two body sizes
//
// What the suite does NOT cover (leave these to backend-specific tests):
//   - Wire-format details (headers, URLs, auth) — backends/*_test.go
//   - Handler-specific behavior (HTTP status codes) — api/*_test.go
//   - Configuration semantics — config/*_test.go
package conformance
