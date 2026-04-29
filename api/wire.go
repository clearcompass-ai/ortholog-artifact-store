package api

import (
	"time"

	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// ─── Canonical wire shapes for HTTP responses ─────────────────────────
//
// The SDK's storage.RetrievalCredential has no JSON struct tags — it
// is a Go-side value type, not a wire shape. Production HTTP traffic
// between artifact-store and SDK consumers (lifecycle.GrantArtifactAccess
// via storage.HTTPRetrievalProvider) flows through an explicit
// lowercase canonical shape — see ortholog-sdk/storage/http_retrieval_
// provider.go's internal resolveResponse.
//
// Encoding storage.RetrievalCredential directly via json.Encoder works
// today only because the SDK's decoder uses case-insensitive matching.
// That works in practice but is a silent dependency on Go's
// encoding/json field-lookup tolerance, and it produces capitalized
// keys ({"Method":...,"URL":...,"Expiry":...}) which a strict consumer
// (a JS client, a JSON-schema validator, or a future SDK build that
// switches to a tag-only matcher) would reject.
//
// resolveResponseV1 is the canonical wire shape. The artifact-store
// always encodes through this struct on the way out; the SDK always
// decodes through its private mirror on the way in. Renaming a field
// here is a wire-format change and must bump the URL prefix (/v1 →
// /v2). Keep this type deliberately spare — it is the contract.

// resolveResponseV1 is the JSON shape returned by GET /v1/artifacts/
// {cid}/resolve. Field names and tags match ortholog-sdk/storage/
// http_retrieval_provider.go resolveResponse exactly.
type resolveResponseV1 struct {
	Method string `json:"method"`
	URL    string `json:"url"`
	Expiry string `json:"expiry,omitempty"`
}

// retrievalCredentialToWire produces the canonical wire shape for a
// storage.RetrievalCredential. Expiry is rendered in RFC3339 (the
// format SDK consumers parse via time.Parse(time.RFC3339, ...)). A
// nil Expiry maps to an omitted "expiry" field, mirroring the
// MethodIPFS / MethodDirect contract that no-expiry retrievals carry
// no expiry field at all.
func retrievalCredentialToWire(c *storage.RetrievalCredential) resolveResponseV1 {
	out := resolveResponseV1{
		Method: c.Method,
		URL:    c.URL,
	}
	if c.Expiry != nil {
		out.Expiry = c.Expiry.UTC().Format(time.RFC3339)
	}
	return out
}

// retrievalCredentialFromWire is the inverse of retrievalCredentialToWire.
// It exists mainly so tests can round-trip without re-implementing the
// SDK's parsing rules — production consumers go through
// storage.HTTPRetrievalProvider, which uses the SDK's own private
// decoder. A nil-Expiry RetrievalCredential is produced when the wire
// shape's "expiry" is empty or unparseable (the SDK treats unparseable
// expiry as nil too — see http_retrieval_provider.go time.Parse path).
func retrievalCredentialFromWire(w resolveResponseV1) *storage.RetrievalCredential {
	out := &storage.RetrievalCredential{
		Method: w.Method,
		URL:    w.URL,
	}
	if w.Expiry != "" {
		if t, err := time.Parse(time.RFC3339, w.Expiry); err == nil {
			out.Expiry = &t
		}
	}
	return out
}
