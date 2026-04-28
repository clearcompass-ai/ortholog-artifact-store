/*
api/token.go — kid-keyed X-Upload-Token verification.

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
      "kid":    "<key-id>"          // operator key identifier — REQUIRED for
                                    // verifier dispatch when the verifier was
                                    // constructed with multiple keys; falls
                                    // back to the empty-string entry for
                                    // single-key deployments.
    }

  signature_bytes is the Ed25519 signature over the raw payload_json
  bytes (not the base64 encoding — sign bytes, not characters).

Verification:
  - kid → operator public key dispatch (rotation-ready). Unknown kid →
    ErrTokenUnknownKid (audit reason "token_unknown_kid").
  - Signature must verify against the resolved operator public key. The
    Ed25519 primitive routes through ortholog-sdk/crypto/signatures
    .VerifyEd25519, which length-checks the pubkey and signature before
    invoking ed25519.Verify (Part 4 v7.75 alignment — single audited
    primitive shared with the rest of the system).
  - Current wall-clock time must be ≤ exp.
  - Payload cid must match the X-Artifact-CID header exactly.
  - Payload size must match the actual body length.

The verifier is stateless — no replay window. A token bound to (cid,
size, exp) is naturally single-meaning under a content-addressed store:
a second push of the same bytes is a no-op. If you need stricter replay
prevention (bounded issuance rate per key), add a nonce field plus a
bounded LRU on the server.

Operator key rotation:
  The verifier accepts a map[kid] → ed25519.PublicKey at construction
  time. During a rotation window, both the old and new operator pubkeys
  live in the map. Tokens minted under either kid verify cleanly. Once
  the operator confirms no in-flight tokens remain under the old kid,
  the old entry is dropped from the map at the next process restart.

  This is the only artifact-store-side machinery for operator key
  rotation. Issuer/role-DID rotations, master identity key rotations,
  and tree-head signing key rotations are handled at the SDK / log /
  domain layer and never touch the artifact store.
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

	"github.com/clearcompass-ai/ortholog-sdk/crypto/signatures"
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

	// ErrTokenUnknownKid is returned when the token's `kid` claim does
	// not match any pubkey registered with the verifier. This is the
	// audit-distinct alternative to ErrTokenBadSignature: the signature
	// hasn't even been checked yet, because we don't know which key to
	// check it against. Audit pipelines should alert on
	// reason="token_unknown_kid" as a strong signal that either an
	// operator key rotation is mid-flight (legitimate) or someone is
	// minting tokens with a kid the artifact store doesn't trust.
	ErrTokenUnknownKid = errors.New("upload token: unknown kid")
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

// ed25519UploadTokenVerifier dispatches verification by the token's
// `kid` claim across a map of operator pubkeys. A single-key deployment
// is just a one-entry map; the empty-string entry, if present, serves
// as the fallback for tokens that omit the kid claim.
type ed25519UploadTokenVerifier struct {
	keys map[string]ed25519.PublicKey
}

// NewEd25519UploadTokenVerifier returns a verifier that selects a
// pubkey by the token's `kid` claim and then verifies the Ed25519
// signature via the SDK's audited primitive.
//
// The keys map MUST be non-nil and contain at least one entry. Each
// pubkey MUST be exactly ed25519.PublicKeySize bytes — the SDK's
// VerifyEd25519 length-checks at every call site, but constructing the
// verifier with a malformed key would just defer the failure to the
// first push attempt; reject early instead.
//
// Pass map["":pubkey] for a single-key deployment that doesn't issue
// kid claims; pass map["op-2026":k1, "op-2027":k2] during a rotation
// window where both keys must be honored simultaneously.
func NewEd25519UploadTokenVerifier(keys map[string]ed25519.PublicKey) (UploadTokenVerifier, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("upload token verifier: keys map is empty")
	}
	out := make(map[string]ed25519.PublicKey, len(keys))
	for kid, pk := range keys {
		if len(pk) != ed25519.PublicKeySize {
			return nil, fmt.Errorf(
				"upload token verifier: key %q is %d bytes, want %d (ed25519)",
				kid, len(pk), ed25519.PublicKeySize)
		}
		out[kid] = pk
	}
	return &ed25519UploadTokenVerifier{keys: out}, nil
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

	// Decode the payload BEFORE signature verification — we need `kid`
	// to pick the pubkey. This is safe: the payload is structured JSON
	// (a parser DoS bound by token-size limits at the HTTP layer), and
	// nothing the unsigned payload tells us is acted on except the
	// pubkey selection. Signature verification still gates everything
	// downstream.
	var p UploadTokenPayload
	if err := json.Unmarshal(payloadBytes, &p); err != nil {
		return fmt.Errorf("%w: json: %v", ErrTokenPayloadInvalid, err)
	}

	pubkey, ok := v.keys[p.Kid]
	if !ok {
		return fmt.Errorf("%w: kid=%q", ErrTokenUnknownKid, p.Kid)
	}

	// Cryptographic verification via the SDK's audited primitive.
	// signatures.VerifyEd25519 length-checks pubkey and signature
	// before invoking ed25519.Verify; a mismatch surfaces as a wrapped
	// error, which we collapse to ErrTokenBadSignature so the audit
	// pipeline sees a single, stable reason.
	if err := signatures.VerifyEd25519(pubkey, payloadBytes, sig); err != nil {
		return fmt.Errorf("%w: %v", ErrTokenBadSignature, err)
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
