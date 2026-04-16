package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// ─── Token builder helper ────────────────────────────────────────────

// mintToken constructs a valid token signed with priv, with the given
// claims. Tests reuse this to build realistic tokens; it mirrors what
// the operator would produce in production.
func mintToken(t *testing.T, priv ed25519.PrivateKey, p UploadTokenPayload) string {
	t.Helper()
	payloadBytes, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	sig := ed25519.Sign(priv, payloadBytes)
	return base64.RawURLEncoding.EncodeToString(payloadBytes) + "." +
		base64.RawURLEncoding.EncodeToString(sig)
}

// newKeypair generates an Ed25519 keypair for tests.
func newKeypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	return pub, priv
}

// ─── Happy path ──────────────────────────────────────────────────────

func TestToken_Verify_HappyPath(t *testing.T) {
	pub, priv := newKeypair(t)
	v := NewEd25519UploadTokenVerifier(pub)

	now := time.Unix(1_700_000_000, 0)
	tok := mintToken(t, priv, UploadTokenPayload{
		CID:  "sha256:deadbeef",
		Size: 1024,
		Exp:  now.Unix() + 300,
		Iat:  now.Unix(),
		Kid:  "operator-key-1",
	})

	if err := v.Verify(tok, "sha256:deadbeef", 1024, now); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
}

// ─── Signature ───────────────────────────────────────────────────────

func TestToken_Verify_BadSignature_WrongKey(t *testing.T) {
	_, priv := newKeypair(t) // signer's key
	otherPub, _ := newKeypair(t) // server trusts a different key
	v := NewEd25519UploadTokenVerifier(otherPub)

	now := time.Now()
	tok := mintToken(t, priv, UploadTokenPayload{
		CID: "sha256:abc", Size: 1, Exp: now.Unix() + 60,
	})

	err := v.Verify(tok, "sha256:abc", 1, now)
	if !errors.Is(err, ErrTokenBadSignature) {
		t.Fatalf("want ErrTokenBadSignature, got %v", err)
	}
}

func TestToken_Verify_BadSignature_PayloadTampered(t *testing.T) {
	pub, priv := newKeypair(t)
	v := NewEd25519UploadTokenVerifier(pub)

	now := time.Now()
	tok := mintToken(t, priv, UploadTokenPayload{
		CID: "sha256:abc", Size: 1, Exp: now.Unix() + 60,
	})

	// Flip one byte of the payload by decoding/re-encoding a mutated version.
	// Since the signature was over the original, verification must fail.
	parts := splitToken(t, tok)
	payloadBytes, _ := base64.RawURLEncoding.DecodeString(parts[0])
	payloadBytes[0] ^= 0x01
	tampered := base64.RawURLEncoding.EncodeToString(payloadBytes) + "." + parts[1]

	err := v.Verify(tampered, "sha256:abc", 1, now)
	if !errors.Is(err, ErrTokenBadSignature) {
		t.Fatalf("want ErrTokenBadSignature, got %v", err)
	}
}

// ─── Expiry ──────────────────────────────────────────────────────────

func TestToken_Verify_Expired(t *testing.T) {
	pub, priv := newKeypair(t)
	v := NewEd25519UploadTokenVerifier(pub)

	now := time.Unix(1_700_000_000, 0)
	tok := mintToken(t, priv, UploadTokenPayload{
		CID: "sha256:abc", Size: 1, Exp: now.Unix() - 1, // already expired
	})

	err := v.Verify(tok, "sha256:abc", 1, now)
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("want ErrTokenExpired, got %v", err)
	}
}

func TestToken_Verify_ZeroExpIsExpired(t *testing.T) {
	pub, priv := newKeypair(t)
	v := NewEd25519UploadTokenVerifier(pub)

	// Absent exp (→ zero after JSON unmarshal) must be rejected, not
	// silently treated as "never expires". Defense-in-depth against a
	// buggy token minter.
	tok := mintToken(t, priv, UploadTokenPayload{
		CID: "sha256:abc", Size: 1, Exp: 0,
	})

	err := v.Verify(tok, "sha256:abc", 1, time.Now())
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("want ErrTokenExpired for exp=0, got %v", err)
	}
}

// ─── Claim mismatches ────────────────────────────────────────────────

func TestToken_Verify_CIDMismatch(t *testing.T) {
	pub, priv := newKeypair(t)
	v := NewEd25519UploadTokenVerifier(pub)

	now := time.Now()
	tok := mintToken(t, priv, UploadTokenPayload{
		CID: "sha256:token-bound-cid", Size: 10, Exp: now.Unix() + 60,
	})

	err := v.Verify(tok, "sha256:different-cid", 10, now)
	if !errors.Is(err, ErrTokenCIDMismatch) {
		t.Fatalf("want ErrTokenCIDMismatch, got %v", err)
	}
}

func TestToken_Verify_SizeMismatch(t *testing.T) {
	pub, priv := newKeypair(t)
	v := NewEd25519UploadTokenVerifier(pub)

	now := time.Now()
	tok := mintToken(t, priv, UploadTokenPayload{
		CID: "sha256:abc", Size: 100, Exp: now.Unix() + 60,
	})

	err := v.Verify(tok, "sha256:abc", 99, now)
	if !errors.Is(err, ErrTokenSizeMismatch) {
		t.Fatalf("want ErrTokenSizeMismatch, got %v", err)
	}
}

// ─── Malformed ───────────────────────────────────────────────────────

func TestToken_Verify_Malformed(t *testing.T) {
	pub, _ := newKeypair(t)
	v := NewEd25519UploadTokenVerifier(pub)

	cases := []struct {
		name string
		tok  string
	}{
		{"empty", ""},
		{"no_dot", "abcdef"},
		{"only_dot", "."},
		{"leading_dot", ".abc"},
		{"trailing_dot", "abc."},
		{"bad_payload_b64", "!!!.abc"},
		{"bad_sig_b64", base64.RawURLEncoding.EncodeToString([]byte(`{"cid":"x"}`)) + ".!!!"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := v.Verify(tc.tok, "sha256:x", 0, time.Now())
			if err == nil {
				t.Fatal("want error, got nil")
			}
			if !errors.Is(err, ErrTokenMalformed) && !errors.Is(err, ErrTokenBadSignature) {
				t.Fatalf("want ErrTokenMalformed or ErrTokenBadSignature, got %v", err)
			}
		})
	}
}

// splitToken is a tiny helper for the tamper test.
func splitToken(t *testing.T, tok string) [2]string {
	t.Helper()
	for i := 0; i < len(tok); i++ {
		if tok[i] == '.' {
			return [2]string{tok[:i], tok[i+1:]}
		}
	}
	t.Fatalf("token has no dot: %q", tok)
	return [2]string{}
}
