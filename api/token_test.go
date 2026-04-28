package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
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

// newSingleKeyVerifier registers a single (kid, pub) pair into a fresh
// verifier. Tests that don't exercise rotation use kid="" so tokens
// minted without a kid claim match. Tests that exercise kid dispatch
// pass an explicit kid here and mint tokens with the same kid.
func newSingleKeyVerifier(t *testing.T, kid string, pub ed25519.PublicKey) UploadTokenVerifier {
	t.Helper()
	v, err := NewEd25519UploadTokenVerifier(map[string]ed25519.PublicKey{kid: pub})
	if err != nil {
		t.Fatalf("NewEd25519UploadTokenVerifier: %v", err)
	}
	return v
}

// ─── Happy path ──────────────────────────────────────────────────────

func TestToken_Verify_HappyPath(t *testing.T) {
	pub, priv := newKeypair(t)
	// Token carries kid="operator-key-1"; register the pubkey under
	// that kid so dispatch finds it.
	v := newSingleKeyVerifier(t, "operator-key-1", pub)

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
	_, priv := newKeypair(t)         // signer's key
	otherPub, _ := newKeypair(t)     // server trusts a different key
	v := newSingleKeyVerifier(t, "", otherPub)

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
	v := newSingleKeyVerifier(t, "", pub)

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
	// Tampering may surface as ErrTokenPayloadInvalid (JSON unmarshal
	// fails before signature check) or ErrTokenBadSignature (signature
	// fails on a still-parseable payload). Both are acceptable.
	if !errors.Is(err, ErrTokenBadSignature) && !errors.Is(err, ErrTokenPayloadInvalid) {
		t.Fatalf("want ErrTokenBadSignature or ErrTokenPayloadInvalid, got %v", err)
	}
}

// ─── Expiry ──────────────────────────────────────────────────────────

func TestToken_Verify_Expired(t *testing.T) {
	pub, priv := newKeypair(t)
	v := newSingleKeyVerifier(t, "", pub)

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
	v := newSingleKeyVerifier(t, "", pub)

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
	v := newSingleKeyVerifier(t, "", pub)

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
	v := newSingleKeyVerifier(t, "", pub)

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
	v := newSingleKeyVerifier(t, "", pub)

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
			// Malformed token paths can surface as Malformed,
			// PayloadInvalid (JSON parse fails), UnknownKid (unsigned
			// payload had a kid we don't recognize), or BadSignature
			// (parseable payload, signature bytes don't verify). All
			// are acceptable rejection modes.
			if !errors.Is(err, ErrTokenMalformed) &&
				!errors.Is(err, ErrTokenPayloadInvalid) &&
				!errors.Is(err, ErrTokenUnknownKid) &&
				!errors.Is(err, ErrTokenBadSignature) {
				t.Fatalf("want a token rejection error, got %v", err)
			}
		})
	}
}

// ─── v7.75: Kid-keyed dispatch (operator key rotation) ───────────────

// TestToken_Verify_KidDispatch_TwoKeysCoexist pins the rotation-window
// invariant: a verifier registered with two operator pubkeys keyed by
// kid honors tokens minted under either one, but not under any other
// kid. This is the core property the artifact store provides during a
// scheduled operator key rotation — tokens issued before and during
// the cutover both verify.
func TestToken_Verify_KidDispatch_TwoKeysCoexist(t *testing.T) {
	pubOld, privOld := newKeypair(t)
	pubNew, privNew := newKeypair(t)

	v, err := NewEd25519UploadTokenVerifier(map[string]ed25519.PublicKey{
		"op-2026": pubOld,
		"op-2027": pubNew,
	})
	if err != nil {
		t.Fatalf("NewEd25519UploadTokenVerifier: %v", err)
	}

	now := time.Now()
	tokOld := mintToken(t, privOld, UploadTokenPayload{
		CID: "sha256:abc", Size: 1, Exp: now.Unix() + 60, Kid: "op-2026",
	})
	tokNew := mintToken(t, privNew, UploadTokenPayload{
		CID: "sha256:def", Size: 1, Exp: now.Unix() + 60, Kid: "op-2027",
	})

	if err := v.Verify(tokOld, "sha256:abc", 1, now); err != nil {
		t.Fatalf("token under op-2026 (old kid): %v", err)
	}
	if err := v.Verify(tokNew, "sha256:def", 1, now); err != nil {
		t.Fatalf("token under op-2027 (new kid): %v", err)
	}
}

// TestToken_Verify_UnknownKid_ReturnsErrTokenUnknownKid pins the
// distinct error sentinel — audit pipelines must be able to alert on
// "unknown kid" separately from "bad signature." An attacker minting
// tokens with a fabricated kid produces this error; the audit reason
// "token_unknown_kid" surfaces in the SIEM.
func TestToken_Verify_UnknownKid_ReturnsErrTokenUnknownKid(t *testing.T) {
	pub, priv := newKeypair(t)
	v := newSingleKeyVerifier(t, "op-known", pub)

	now := time.Now()
	tok := mintToken(t, priv, UploadTokenPayload{
		CID: "sha256:abc", Size: 1, Exp: now.Unix() + 60, Kid: "op-attacker",
	})

	err := v.Verify(tok, "sha256:abc", 1, now)
	if !errors.Is(err, ErrTokenUnknownKid) {
		t.Fatalf("want ErrTokenUnknownKid, got %v", err)
	}
}

// TestToken_Verify_TokenWithoutKid_RequiresEmptyEntry pins the single-
// key deployment story: a verifier that registers a pubkey under
// kid="" accepts tokens that omit the kid claim. Without that empty
// entry, a token without kid is rejected as unknown.
func TestToken_Verify_TokenWithoutKid_RequiresEmptyEntry(t *testing.T) {
	pub, priv := newKeypair(t)

	now := time.Now()
	tok := mintToken(t, priv, UploadTokenPayload{
		CID: "sha256:abc", Size: 1, Exp: now.Unix() + 60,
		// no Kid set — JSON omits the field
	})

	t.Run("with_empty_entry_accepts", func(t *testing.T) {
		v := newSingleKeyVerifier(t, "", pub)
		if err := v.Verify(tok, "sha256:abc", 1, now); err != nil {
			t.Fatalf("kid-less token with empty-entry verifier: %v", err)
		}
	})
	t.Run("without_empty_entry_rejects", func(t *testing.T) {
		v := newSingleKeyVerifier(t, "op-required", pub)
		err := v.Verify(tok, "sha256:abc", 1, now)
		if !errors.Is(err, ErrTokenUnknownKid) {
			t.Fatalf("want ErrTokenUnknownKid, got %v", err)
		}
	})
}

// TestToken_Verify_KidDispatch_WrongKeyForKid pins fail-closed under
// kid spoofing: an attacker who takes a token signed by the OLD key
// and rewrites its kid claim to point at the NEW key's slot must NOT
// verify, because the signature was made with the old private key.
// The rewritten payload's signature won't match the new pubkey.
func TestToken_Verify_KidDispatch_WrongKeyForKid(t *testing.T) {
	pubOld, privOld := newKeypair(t)
	pubNew, _ := newKeypair(t)

	v, err := NewEd25519UploadTokenVerifier(map[string]ed25519.PublicKey{
		"op-old": pubOld,
		"op-new": pubNew,
	})
	if err != nil {
		t.Fatalf("NewEd25519UploadTokenVerifier: %v", err)
	}

	now := time.Now()
	// Mint with old key but claim kid=op-new.
	tok := mintToken(t, privOld, UploadTokenPayload{
		CID: "sha256:abc", Size: 1, Exp: now.Unix() + 60, Kid: "op-new",
	})

	err = v.Verify(tok, "sha256:abc", 1, now)
	if !errors.Is(err, ErrTokenBadSignature) {
		t.Fatalf("want ErrTokenBadSignature, got %v", err)
	}
}

// ─── v7.75: Constructor input validation ─────────────────────────────

func TestNewEd25519UploadTokenVerifier_RejectsEmptyMap(t *testing.T) {
	_, err := NewEd25519UploadTokenVerifier(map[string]ed25519.PublicKey{})
	if err == nil {
		t.Fatal("empty map: want error, got nil")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("error should mention empty map: %v", err)
	}
}

func TestNewEd25519UploadTokenVerifier_RejectsWrongLengthKey(t *testing.T) {
	// 16 bytes — clearly not Ed25519 (which is 32).
	short := make(ed25519.PublicKey, 16)
	_, err := NewEd25519UploadTokenVerifier(map[string]ed25519.PublicKey{
		"shorty": short,
	})
	if err == nil {
		t.Fatal("short key: want error, got nil")
	}
	if !strings.Contains(err.Error(), "ed25519") {
		t.Fatalf("error should mention ed25519: %v", err)
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
