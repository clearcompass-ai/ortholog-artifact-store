/*
api/token.go — optional X-Upload-Token verification.

Token contract:
  The header value is a dot-separated JSON payload and signature, both
  base64url (RFC 4648 section 5) encoded:

    X-Upload-Token: <base64url(payload_json)>.<base64url(signature_bytes)>

  payload_json decodes to:
    {
      "cid":    "sha256:<hex>",    // must equal X-Artifact-CID header
      "size":   <int>,              // must equal body length in bytes
      "exp":    <unix_seconds>,     // token expiration
      "iat":    <unix_seconds>,     // issued-at (optional, informational)
      "kid":    "<key-id>"          // operator key identifier (informational)
    }

  signature_bytes is the Ed25519 signature over the raw payload_json
  bytes (not the base64 encoding — sign bytes, not characters).

Verification:
  - Signature must verify against the operator's registered public key.
  - Current wall-clock time must be ≤ exp.
  - Payload cid must match the X-Artifact-CID header exactly.
  - Payload size must match the actual body length.

The token is single-use only implicitly — the server does not track a
replay window. Since a token is cryptographically bound to (cid, size,
exp) and pushes are idempotent by CID, replay is harmless: a second push
of the same bytes is a no-op to the content-addressed store.

If you need stricter replay prevention (e.g., bounded issuance rate per
key), add a nonce field + a bounded LRU on the server. Not done here;
the operator already enforces upstream quotas.
*/
package api

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)
// UploadTokenVerifier validates an X-Upload-Token against the expected
// CID, size, and the current time. Implementations should return nil on
// success; any non-nil error is logged and the push is rejected (401).
type UploadTokenVerifier interface {
	Verify(token, expectedCID string, actualSize int64, now time.Time) error
}

// ─── Errors ──────────────────────────────────────────────────────────

// Sentinel errors. Tests and logs discriminate on these via errors.Is.
var (
	ErrTokenMalformed      = errors.New("upload token: malformed")
	ErrTokenBadSignature   = errors.New("upload token: bad signature")
	ErrTokenExpired        = errors.New("upload token: expired")
	ErrTokenCIDMismatch    = errors.New("upload token: cid mismatch")
	ErrTokenSizeMismatch   = errors.New("upload token: size mismatch")
	ErrTokenPayloadInvalid = errors.New("upload token: payload invalid")
)

// ─── Payload ─────────────────────────────────────────────────────────

// UploadTokenPayload is the JSON structure carried in the token.
type UploadTokenPayload struct {
	CID  string `json:"cid"`
	Size int64  `json:"size"`
	Exp  int64  `json:"exp"`
	Iat  int64  `json:"iat,omitempty"`
	Kid  string `json:"kid,omitempty"`
}

// ─── Ed25519 verifier ────────────────────────────────────────────────

// ed25519UploadTokenVerifier implements UploadTokenVerifier against a
// single operator public key. For multi-operator deployments, wrap this
// in a verifier that dispatches by the payload's `kid` field; not done
// here because OP-9 is single-operator.
type ed25519UploadTokenVerifier struct {
	pubkey ed25519.PublicKey
}

// NewEd25519UploadTokenVerifier returns a verifier that checks Ed25519
// signatures against the given public key.
func NewEd25519UploadTokenVerifier(pubkey ed25519.PublicKey) UploadTokenVerifier {
	return &ed25519UploadTokenVerifier{pubkey: pubkey}
}

func (v *ed25519UploadTokenVerifier) Verify(token, expectedCID string, actualSize int64, now time.Time) error {
	// Split "payload.signature".
	dot := strings.IndexByte(token, '.')
	if dot < 1 || dot == len(token)-1 {
		return fmt.Errorf("%w: expected payload.signature", ErrTokenMalformed)
	}
	payloadB64 := token[:dot]
	sigB64 := token[dot+1:]

	// Decode the payload bytes (raw JSON) and signature bytes.
	payloadBytes, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		// Fall back to std base64 for tolerance with generators that pad.
		payloadBytes, err = base64.URLEncoding.DecodeString(payloadB64)
		if err != nil {
			return fmt.Errorf("%w: payload base64: %v", ErrTokenMalformed, err)
		}
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		sig, err = base64.URLEncoding.DecodeString(sigB64)
		if err != nil {
			return fmt.Errorf("%w: signature base64: %v", ErrTokenMalformed, err)
		}
	}

	// Cryptographic verification first — any malformed or tampered token
	// fails here. We refuse to inspect claims from an unsigned payload.
	if !ed25519.Verify(v.pubkey, payloadBytes, sig) {
		return ErrTokenBadSignature
	}

	// Decode the payload.
	var p UploadTokenPayload
	if err := json.Unmarshal(payloadBytes, &p); err != nil {
		return fmt.Errorf("%w: json: %v", ErrTokenPayloadInvalid, err)
	}

	// Claims checks. Order matters only for the error the operator sees.
	if p.Exp == 0 || now.Unix() > p.Exp {
		return ErrTokenExpired
	}
	if p.CID != expectedCID {
		return fmt.Errorf("%w: token cid=%s, header cid=%s", ErrTokenCIDMismatch, p.CID, expectedCID)
	}
	if p.Size != actualSize {
		return fmt.Errorf("%w: token size=%d, body size=%d", ErrTokenSizeMismatch, p.Size, actualSize)
	}
	return nil
}
