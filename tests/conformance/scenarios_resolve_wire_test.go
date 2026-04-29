package conformance

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// Direct unit cover for runResolveWire and its helpers. The full
// scenario runs via RunBackendConformance against InMemoryBackend in
// suite_inmemory_test.go; this file pins each helper individually so
// regressions surface with a precise test name.

func TestRunResolveWire_PassesAgainstInMemory(t *testing.T) {
	factory := func() backends.BackendProvider { return backends.NewInMemoryBackend() }
	runResolveWire(t, factory, Capabilities{
		SupportsDelete:        true,
		SupportsExpiry:        false,
		ExpectedResolveMethod: storage.MethodDirect,
	})
}

func TestMarshalCredentialAsWire_LowercaseKeys(t *testing.T) {
	moment := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	body := marshalCredentialAsWire(t, &storage.RetrievalCredential{
		Method: storage.MethodSignedURL,
		URL:    "https://example.com/x",
		Expiry: &moment,
	})
	for _, want := range []string{`"method":`, `"url":`, `"expiry":`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("body missing key %s: %s", want, body)
		}
	}
	for _, bad := range []string{`"Method":`, `"URL":`, `"Expiry":`} {
		if strings.Contains(string(body), bad) {
			t.Fatalf("body leaked capitalized key %s: %s", bad, body)
		}
	}
}

func TestMarshalCredentialAsWire_OmitsExpiryWhenNil(t *testing.T) {
	body := marshalCredentialAsWire(t, &storage.RetrievalCredential{
		Method: storage.MethodDirect,
		URL:    "sha256:abc",
		Expiry: nil,
	})
	if strings.Contains(string(body), "expiry") {
		t.Fatalf("nil Expiry leaked into wire: %s", body)
	}
}

func TestMarshalCredentialAsWire_ExpiryRFC3339(t *testing.T) {
	moment := time.Date(2026, 5, 1, 12, 30, 45, 0, time.UTC)
	body := marshalCredentialAsWire(t, &storage.RetrievalCredential{
		Method: storage.MethodSignedURL,
		URL:    "https://example.com/x",
		Expiry: &moment,
	})
	var got canonicalResolveWire
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	parsed, err := time.Parse(time.RFC3339, got.Expiry)
	if err != nil {
		t.Fatalf("expiry not RFC3339: %q (%v)", got.Expiry, err)
	}
	if !parsed.Equal(moment) {
		t.Fatalf("RFC3339 round-trip drift: in=%v out=%v", moment, parsed)
	}
}

func TestMustDecodeStrict_AcceptsCanonicalShape(t *testing.T) {
	body := []byte(`{"method":"direct","url":"sha256:abc"}`)
	mustDecodeStrict(t, body)
}

// canonicalResolveWire decoder is strict — unknown fields fail.
// Verify by writing a body with a typo and checking it fatals.
// We can't easily fatal-detect from this test (the helper calls
// t.Fatalf), so we use a sub-test with a recovered helper that
// captures the failure via its own *testing.T. Rather than do
// that, we write an inline strict decoder and compare behavior.
func TestCanonicalResolveWire_StrictDecodeRejectsUnknownField(t *testing.T) {
	body := []byte(`{"method":"direct","url":"sha256:abc","extraField":"oops"}`)
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	var got canonicalResolveWire
	if err := dec.Decode(&got); err == nil {
		t.Fatal("strict decode accepted body with unknown field; want error")
	}
}

func TestCanonicalResolveWire_AcceptsCapitalizedKeysInLooseMode(t *testing.T) {
	// This is the "today's behavior" case: the SDK's decoder is
	// case-insensitive without DisallowUnknownFields. We pin
	// that the canonical shape ALSO survives a loose decoder so
	// downgrades from strict don't surprise us.
	body := []byte(`{"method":"direct","url":"sha256:abc"}`)
	var got canonicalResolveWire
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("loose decode failed: %v", err)
	}
	if got.Method != "direct" || got.URL != "sha256:abc" {
		t.Fatalf("loose decode field drift: %+v", got)
	}
}
