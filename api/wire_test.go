package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// Wire-shape contract tests. Anything in here that fails is a
// breaking change to the resolve endpoint's HTTP body and requires
// either a coordinated SDK release or a /v2 URL bump.

func TestResolveWire_FieldsMatchSDKLowercase(t *testing.T) {
	// The artifact-store-emitted shape MUST use lowercase keys
	// "method", "url", "expiry". The SDK's decoder works around
	// capitalized keys today via case-insensitive matching, but
	// strict downstream consumers would reject.
	wireRaw, err := json.Marshal(retrievalCredentialToWire(&storage.RetrievalCredential{
		Method: storage.MethodSignedURL,
		URL:    "https://example.com/blob",
		Expiry: timePtr(time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)),
	}))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := string(wireRaw)
	for _, key := range []string{`"method":`, `"url":`, `"expiry":`} {
		if !strings.Contains(got, key) {
			t.Fatalf("wire shape missing key %s; got %s", key, got)
		}
	}
	for _, capKey := range []string{`"Method":`, `"URL":`, `"Expiry":`} {
		if strings.Contains(got, capKey) {
			t.Fatalf("wire shape leaked capitalized key %s; got %s", capKey, got)
		}
	}
}

func TestResolveWire_OmitsExpiryWhenNil(t *testing.T) {
	// MethodDirect / MethodIPFS have no expiry. The SDK's
	// resolveResponse uses ",omitempty" — match exactly.
	wireRaw, err := json.Marshal(retrievalCredentialToWire(&storage.RetrievalCredential{
		Method: storage.MethodDirect,
		URL:    "sha256:abc",
		Expiry: nil,
	}))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(wireRaw), "expiry") {
		t.Fatalf("nil Expiry leaked into JSON: %s", wireRaw)
	}
}

func TestResolveWire_ExpiryFormatIsRFC3339(t *testing.T) {
	// The SDK's HTTPRetrievalProvider parses expiry via
	// time.Parse(time.RFC3339, ...). Encode in the same format
	// or the SDK silently drops the expiry to nil.
	moment := time.Date(2026, 5, 1, 12, 30, 45, 0, time.UTC)
	w := retrievalCredentialToWire(&storage.RetrievalCredential{
		Method: storage.MethodSignedURL,
		URL:    "https://example.com",
		Expiry: &moment,
	})
	parsed, err := time.Parse(time.RFC3339, w.Expiry)
	if err != nil {
		t.Fatalf("RFC3339 parse failed on emitted expiry %q: %v", w.Expiry, err)
	}
	if !parsed.Equal(moment) {
		t.Fatalf("expiry round-trip drift: in=%v out=%v", moment, parsed)
	}
}

func TestResolveWire_RoundTripPreservesFields(t *testing.T) {
	moment := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	original := &storage.RetrievalCredential{
		Method: storage.MethodSignedURL,
		URL:    "https://signed-url.example.com/blob?sig=abc",
		Expiry: &moment,
	}
	wire := retrievalCredentialToWire(original)
	out := retrievalCredentialFromWire(wire)
	if out.Method != original.Method {
		t.Fatalf("Method drift: in=%q out=%q", original.Method, out.Method)
	}
	if out.URL != original.URL {
		t.Fatalf("URL drift: in=%q out=%q", original.URL, out.URL)
	}
	if out.Expiry == nil || !out.Expiry.Equal(*original.Expiry) {
		t.Fatalf("Expiry drift: in=%v out=%v", original.Expiry, out.Expiry)
	}
}

func TestResolveWire_NilExpiryRoundTripsAsNil(t *testing.T) {
	original := &storage.RetrievalCredential{
		Method: storage.MethodDirect,
		URL:    "sha256:abc",
		Expiry: nil,
	}
	wire := retrievalCredentialToWire(original)
	out := retrievalCredentialFromWire(wire)
	if out.Expiry != nil {
		t.Fatalf("nil Expiry round-tripped to %v", out.Expiry)
	}
}

// TestResolveWire_DecodesAsSDKResolveResponse mirrors the SDK's
// private resolveResponse struct shape locally and asserts that
// the artifact-store's wire bytes decode cleanly through it. This
// is the strict half of the contract: no case-insensitivity fallback,
// no key aliasing — exact lowercase match.
func TestResolveWire_DecodesAsSDKResolveResponse(t *testing.T) {
	type sdkResolveResponse struct {
		Method string `json:"method"`
		URL    string `json:"url"`
		Expiry string `json:"expiry,omitempty"`
	}
	moment := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	body, err := json.Marshal(retrievalCredentialToWire(&storage.RetrievalCredential{
		Method: storage.MethodSignedURL,
		URL:    "https://example.com/x",
		Expiry: &moment,
	}))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got sdkResolveResponse
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("strict SDK-shape decode failed: %v\nbody=%s", err, body)
	}
	if got.Method != storage.MethodSignedURL {
		t.Fatalf("Method mismatch: %q", got.Method)
	}
	if got.URL != "https://example.com/x" {
		t.Fatalf("URL mismatch: %q", got.URL)
	}
	if got.Expiry != moment.Format(time.RFC3339) {
		t.Fatalf("Expiry mismatch: %q", got.Expiry)
	}
}

func timePtr(t time.Time) *time.Time { return &t }
