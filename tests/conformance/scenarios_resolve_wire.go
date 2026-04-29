package conformance

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// ─── Resolve credential ↔ SDK wire-shape contract ─────────────────────
//
// The artifact-store HTTP layer encodes backend.Resolve()'s output into
// the canonical wire shape (api/wire.go resolveResponseV1) and the SDK
// decodes it through its private mirror in storage/http_retrieval_
// provider.go (resolveResponse). Both shapes use lowercase JSON keys
// with json tags, ",omitempty" on Expiry, and RFC3339 expiry encoding.
//
// This scenario pins that wire-format contract per backend, without
// involving HTTP. The integration-level HTTP wire test
// (tests/integration/suite_sdk_wire_test.go) covers the full path
// including a real SDK HTTPRetrievalProvider; this conformance run
// catches drift before the integration tests need a container.
//
// The struct below is a private mirror of the SDK's resolveResponse —
// duplicating it in the test package gives us a strict
// DisallowUnknownFields decoder that fails immediately on key drift,
// instead of silently accepting whatever the SDK's case-insensitive
// matcher tolerates.

// canonicalResolveWire mirrors ortholog-sdk/storage/http_retrieval_
// provider.go resolveResponse. Field names and tags MUST stay in
// sync; if the SDK changes its wire shape, this struct fails the
// scenarios first.
type canonicalResolveWire struct {
	Method string `json:"method"`
	URL    string `json:"url"`
	Expiry string `json:"expiry,omitempty"`
}

func runResolveWire(t *testing.T, factory Factory, caps Capabilities) {
	t.Run("credential_marshals_to_canonical_wire_shape", func(t *testing.T) {
		b := factory()
		data := []byte("resolve-wire/canonical")
		cid := storage.Compute(data)
		if err := b.Push(cid, data); err != nil {
			t.Fatalf("Push: %v", err)
		}

		cred, err := b.Resolve(cid, time.Hour)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}

		body := marshalCredentialAsWire(t, cred)
		mustDecodeStrict(t, body)
	})

	t.Run("wire_shape_uses_lowercase_keys", func(t *testing.T) {
		// Strict assertion that the encoded JSON contains the
		// SDK-required lowercase keys and not the Go-default
		// capitalized variants.
		b := factory()
		data := []byte("resolve-wire/keys")
		cid := storage.Compute(data)
		if err := b.Push(cid, data); err != nil {
			t.Fatalf("Push: %v", err)
		}
		cred, err := b.Resolve(cid, time.Hour)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		body := marshalCredentialAsWire(t, cred)
		assertContains(t, body, `"method":`)
		assertContains(t, body, `"url":`)
		assertNotContains(t, body, `"Method":`)
		assertNotContains(t, body, `"URL":`)
	})

	if caps.SupportsExpiry {
		t.Run("wire_expiry_is_rfc3339_and_round_trips", func(t *testing.T) {
			b := factory()
			data := []byte("resolve-wire/expiry")
			cid := storage.Compute(data)
			if err := b.Push(cid, data); err != nil {
				t.Fatalf("Push: %v", err)
			}
			cred, err := b.Resolve(cid, time.Hour)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if cred.Expiry == nil {
				t.Fatal("backend declared SupportsExpiry but returned nil Expiry")
			}

			body := marshalCredentialAsWire(t, cred)
			var got canonicalResolveWire
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("decode wire body: %v", err)
			}
			if got.Expiry == "" {
				t.Fatal("wire shape omitted expiry for a SupportsExpiry backend")
			}
			parsed, perr := time.Parse(time.RFC3339, got.Expiry)
			if perr != nil {
				t.Fatalf("expiry not RFC3339: %q (%v)", got.Expiry, perr)
			}
			if !parsed.Equal(cred.Expiry.UTC().Truncate(time.Second)) {
				t.Fatalf("expiry round-trip drift: in=%v out=%v", cred.Expiry, parsed)
			}
		})
	} else {
		t.Run("wire_expiry_omitted_for_no_expiry_backend", func(t *testing.T) {
			b := factory()
			data := []byte("resolve-wire/no-expiry")
			cid := storage.Compute(data)
			if err := b.Push(cid, data); err != nil {
				t.Fatalf("Push: %v", err)
			}
			cred, err := b.Resolve(cid, time.Hour)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			body := marshalCredentialAsWire(t, cred)
			if strings.Contains(string(body), `"expiry":`) {
				t.Fatalf("wire shape leaked expiry for no-expiry backend: %s", body)
			}
		})
	}

	t.Run("method_field_matches_caps", func(t *testing.T) {
		// The Method byte the backend stamps on the credential
		// must round-trip through the wire shape unchanged. A
		// regression where toWire silently rewrites the Method
		// (e.g. legacy "signed-url" → "signed_url") is caught
		// by the body-level string match.
		b := factory()
		data := []byte("resolve-wire/method")
		cid := storage.Compute(data)
		if err := b.Push(cid, data); err != nil {
			t.Fatalf("Push: %v", err)
		}
		cred, err := b.Resolve(cid, time.Hour)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		body := marshalCredentialAsWire(t, cred)
		var got canonicalResolveWire
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Method != caps.ExpectedResolveMethod {
			t.Fatalf("wire Method=%q, caps.ExpectedResolveMethod=%q", got.Method, caps.ExpectedResolveMethod)
		}
	})
}

// marshalCredentialAsWire encodes a *storage.RetrievalCredential into
// the canonical lowercase wire shape — the same encoding api/resolve.go
// uses on the production HTTP path. Duplicated here (not imported) to
// keep the conformance package free of HTTP-layer dependencies.
func marshalCredentialAsWire(t *testing.T, c *storage.RetrievalCredential) []byte {
	t.Helper()
	w := canonicalResolveWire{Method: c.Method, URL: c.URL}
	if c.Expiry != nil {
		w.Expiry = c.Expiry.UTC().Format(time.RFC3339)
	}
	body, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("Marshal canonical wire: %v", err)
	}
	return body
}

// mustDecodeStrict decodes the body through a fresh canonicalResolveWire
// with DisallowUnknownFields. Any extra/different keys = test failure.
func mustDecodeStrict(t *testing.T, body []byte) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	var got canonicalResolveWire
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("strict canonical-shape decode failed: %v\nbody=%s", err, body)
	}
}

func assertContains(t *testing.T, body []byte, substr string) {
	t.Helper()
	if !strings.Contains(string(body), substr) {
		t.Fatalf("body missing %q: %s", substr, body)
	}
}

func assertNotContains(t *testing.T, body []byte, substr string) {
	t.Helper()
	if strings.Contains(string(body), substr) {
		t.Fatalf("body contains %q (regression): %s", substr, body)
	}
}
