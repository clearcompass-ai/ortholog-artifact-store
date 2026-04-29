package api

import (
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"

	"github.com/clearcompass-ai/ortholog-sdk/crypto/signatures"
)

// ─── SDK signatures.VerifyEd25519 contract ────────────────────────────
//
// api/token.go's ed25519UploadTokenVerifier delegates Ed25519 signature
// verification to the SDK's audited primitive:
//
//   if err := signatures.VerifyEd25519(pubkey, payloadBytes, sig); err != nil {
//       return fmt.Errorf("%w: %v", ErrTokenBadSignature, err)
//   }
//
// The wrapper collapses every SDK error into the single
// ErrTokenBadSignature audit reason. That collapse is correct as long
// as the SDK's contract is:
//
//   1. Returns nil on (valid pubkey, valid sig over the message).
//   2. Returns a non-nil error on EVERY failure mode (length-checks,
//      malformed pubkey, bad signature) — never panics, never returns
//      (nil, nil) on a failure.
//
// If a future SDK refactor accidentally returns nil on length-mismatch
// instead of an error, the artifact-store's audit pipeline silently
// stops emitting "token_invalid" rejections. These tests pin the SDK
// contract artifact-store wraps. A drift in the SDK fails this test
// before token_test.go's wrapper-level tests do.

func TestSDKEd25519_ValidSignatureVerifies(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	msg := []byte("contract/sdk-ed25519/valid")
	sig := ed25519.Sign(priv, msg)

	if err := signatures.VerifyEd25519(pub, msg, sig); err != nil {
		t.Fatalf("VerifyEd25519 on valid sig: err=%v, want nil", err)
	}
}

func TestSDKEd25519_BadSignatureReturnsVerificationFailed(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	msg := []byte("contract/sdk-ed25519/bad-sig")
	badSig := make([]byte, ed25519.SignatureSize) // all zero — definitely not a valid signature

	err = signatures.VerifyEd25519(pub, msg, badSig)
	if err == nil {
		t.Fatal("VerifyEd25519 on zero-byte signature: want error, got nil")
	}
	if !errors.Is(err, signatures.ErrSignatureVerificationFailed) {
		t.Fatalf("err=%v, want errors.Is(err, ErrSignatureVerificationFailed)", err)
	}
}

func TestSDKEd25519_NilPubkeyReturnsInvalidPublicKey(t *testing.T) {
	msg := []byte("contract/sdk-ed25519/nil-pub")
	sig := make([]byte, ed25519.SignatureSize)

	err := signatures.VerifyEd25519(nil, msg, sig)
	if err == nil {
		t.Fatal("VerifyEd25519 with nil pubkey: want error, got nil")
	}
	if !errors.Is(err, signatures.ErrInvalidPublicKey) {
		t.Fatalf("err=%v, want errors.Is(err, ErrInvalidPublicKey)", err)
	}
}

func TestSDKEd25519_ShortPubkeyReturnsInvalidPublicKey(t *testing.T) {
	msg := []byte("contract/sdk-ed25519/short-pub")
	sig := make([]byte, ed25519.SignatureSize)

	err := signatures.VerifyEd25519(make([]byte, 16), msg, sig)
	if err == nil {
		t.Fatal("VerifyEd25519 with 16-byte pubkey: want error, got nil")
	}
	if !errors.Is(err, signatures.ErrInvalidPublicKey) {
		t.Fatalf("err=%v, want errors.Is(err, ErrInvalidPublicKey)", err)
	}
}

func TestSDKEd25519_LongPubkeyReturnsInvalidPublicKey(t *testing.T) {
	msg := []byte("contract/sdk-ed25519/long-pub")
	sig := make([]byte, ed25519.SignatureSize)

	err := signatures.VerifyEd25519(make([]byte, 64), msg, sig)
	if err == nil {
		t.Fatal("VerifyEd25519 with 64-byte pubkey: want error, got nil")
	}
	if !errors.Is(err, signatures.ErrInvalidPublicKey) {
		t.Fatalf("err=%v, want errors.Is(err, ErrInvalidPublicKey)", err)
	}
}

func TestSDKEd25519_ShortSignatureReturnsLengthError(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	msg := []byte("contract/sdk-ed25519/short-sig")

	err = signatures.VerifyEd25519(pub, msg, make([]byte, 16))
	if err == nil {
		t.Fatal("VerifyEd25519 with 16-byte sig: want error, got nil")
	}
	// The SDK uses a plain fmt.Errorf for short signatures (no wrapping
	// sentinel today). Pin the message shape so a regression that
	// drops the length error surfaces here.
	if !strings.Contains(err.Error(), "Ed25519 signature must be") {
		t.Fatalf("err=%v, want message mentioning Ed25519 signature length", err)
	}
}

func TestSDKEd25519_LongSignatureReturnsLengthError(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	msg := []byte("contract/sdk-ed25519/long-sig")

	err = signatures.VerifyEd25519(pub, msg, make([]byte, 128))
	if err == nil {
		t.Fatal("VerifyEd25519 with 128-byte sig: want error, got nil")
	}
	if !strings.Contains(err.Error(), "Ed25519 signature must be") {
		t.Fatalf("err=%v, want length error", err)
	}
}

func TestSDKEd25519_DifferentMessageDoesNotVerify(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	signedMsg := []byte("contract/sdk-ed25519/signed")
	sig := ed25519.Sign(priv, signedMsg)

	tamperedMsg := []byte("contract/sdk-ed25519/tampered")
	err = signatures.VerifyEd25519(pub, tamperedMsg, sig)
	if !errors.Is(err, signatures.ErrSignatureVerificationFailed) {
		t.Fatalf("err=%v, want ErrSignatureVerificationFailed for tampered message", err)
	}
}

func TestSDKEd25519_DifferentPubkeyDoesNotVerify(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	otherPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	msg := []byte("contract/sdk-ed25519/wrong-pub")
	sig := ed25519.Sign(priv, msg)

	err = signatures.VerifyEd25519(otherPub, msg, sig)
	if !errors.Is(err, signatures.ErrSignatureVerificationFailed) {
		t.Fatalf("err=%v, want ErrSignatureVerificationFailed for unrelated pubkey", err)
	}
}

// TestSDKEd25519_NilSignatureReturnsLengthError pins the nil-sig path.
// VerifyEd25519's length check runs len(sig) == 0 if sig is nil, so the
// length error fires (not a panic).
func TestSDKEd25519_NilSignatureReturnsLengthError(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	err = signatures.VerifyEd25519(pub, []byte("any"), nil)
	if err == nil {
		t.Fatal("VerifyEd25519 with nil sig: want error, got nil")
	}
	if !strings.Contains(err.Error(), "Ed25519 signature must be") {
		t.Fatalf("err=%v, want length error", err)
	}
}

// TestSDKEd25519_EmptyMessageVerifies pins that an empty message is
// a valid Ed25519 input. Some upload-token implementations might
// produce zero-byte payloads in degenerate cases; this confirms the
// SDK doesn't choke on len(msg)==0.
func TestSDKEd25519_EmptyMessageVerifies(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	sig := ed25519.Sign(priv, nil)
	if err := signatures.VerifyEd25519(pub, nil, sig); err != nil {
		t.Fatalf("VerifyEd25519 on empty message: %v", err)
	}
}
