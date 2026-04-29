package conformance

import (
	"testing"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// Direct unit cover for the helpers in scenarios_cid_parse.go. The
// scenario itself exercises through the full RunBackendConformance
// path against InMemoryBackend in suite_inmemory_test.go; this file
// pins each helper so a regression in one helper surfaces with a
// specific test name rather than buried under a generic "InMemory/
// CIDParse/..." failure.

func TestRunCIDParse_PassesAgainstInMemory(t *testing.T) {
	factory := func() backends.BackendProvider { return backends.NewInMemoryBackend() }
	runCIDParse(t, factory, Capabilities{
		SupportsDelete:        true,
		SupportsExpiry:        false,
		ExpectedResolveMethod: storage.MethodDirect,
	})
}

func TestAssertStringRoundTrip_SHA256(t *testing.T) {
	factory := func() backends.BackendProvider { return backends.NewInMemoryBackend() }
	assertStringRoundTrip(t, factory, []byte("unit-string-roundtrip-sha256"), storage.AlgoSHA256)
}

func TestAssertStringRoundTrip_CustomAlgorithm(t *testing.T) {
	registerWireFormTestAlgorithm()
	factory := func() backends.BackendProvider { return backends.NewInMemoryBackend() }
	assertStringRoundTrip(t, factory, []byte("unit-string-roundtrip-custom"), wireFormTestAlgoTag)
}

func TestAssertBytesRoundTrip_SHA256(t *testing.T) {
	factory := func() backends.BackendProvider { return backends.NewInMemoryBackend() }
	assertBytesRoundTrip(t, factory, []byte("unit-bytes-roundtrip-sha256"), storage.AlgoSHA256)
}

func TestAssertBytesRoundTrip_CustomAlgorithm(t *testing.T) {
	registerWireFormTestAlgorithm()
	factory := func() backends.BackendProvider { return backends.NewInMemoryBackend() }
	assertBytesRoundTrip(t, factory, []byte("unit-bytes-roundtrip-custom"), wireFormTestAlgoTag)
}

// TestCIDParse_StringPrefixMatchesSDK locks the string-form prefix
// invariant the api/push.go algorithmName helper depends on. If the
// SDK ever rewrites cid.String() (e.g. multibase, double-colon), the
// audit field cid_algorithm starts emitting empty strings — this
// test fails first.
func TestCIDParse_StringPrefixMatchesSDK(t *testing.T) {
	cid := storage.Compute([]byte("prefix-test"))
	s := cid.String()
	if len(s) < len("sha256:") || s[:len("sha256:")] != "sha256:" {
		t.Fatalf("CID.String() = %q, want prefix %q", s, "sha256:")
	}
}

// TestCIDParse_RejectsUnregisteredName provides a regression-test
// pair to the scenario-level "parse_rejects_unknown_algorithm_name"
// — if the SDK switches to lenient parsing, both fail loudly.
func TestCIDParse_RejectsUnregisteredName(t *testing.T) {
	if _, err := storage.ParseCID("not-a-real-algo:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"); err == nil {
		t.Fatal("ParseCID accepted unregistered algorithm name; want error")
	}
}
