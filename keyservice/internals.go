package keyservice

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"errors"
	"fmt"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// aesGCMSeal matches crypto/artifact.EncryptArtifact's wire layout
// exactly: gcm.Seal(nil, nonce, plaintext, nil). The nonce is NOT
// prepended to the output — it travels with the wrapped key
// (recipient receives both via WrapForRecipient).
//
// The recipient calls crypto/artifact.DecryptArtifact(ciphertext,
// ArtifactKey{Key, Nonce}); that reads nonce from the struct, not
// from the ciphertext prefix.
func aesGCMSeal(key, nonce, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != aead.NonceSize() {
		return nil, fmt.Errorf("keyservice: nonce length %d, want %d", len(nonce), aead.NonceSize())
	}
	return aead.Seal(nil, nonce, plaintext, nil), nil
}

// aesGCMOpen reverses aesGCMSeal — runs gcm.Open with the supplied
// nonce against the full ciphertext (no embedded nonce prefix to
// strip). Mirrors crypto/artifact.DecryptArtifact byte-for-byte.
//
// Validates nonce length up-front: crypto/cipher's gcm.Open panics
// (rather than returning an error) on the wrong nonce size, and we
// don't want a malformed input to crash the keyservice goroutine.
func aesGCMOpen(key, nonce, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != aead.NonceSize() {
		return nil, fmt.Errorf("keyservice: nonce length %d, want %d", len(nonce), aead.NonceSize())
	}
	pt, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, err
	}
	return pt, nil
}

// zeroize overwrites b with zero bytes. Used to wipe key material from
// process memory after use. The Go compiler does not optimize this
// out for the slice's backing array (it's an actual write through a
// non-elidable pointer).
func zeroize(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// parseRecipientPubKey parses a SEC1-encoded secp256k1 public key
// (uncompressed: 0x04 || X || Y; compressed: 0x02|0x03 || X) into
// the *ecdsa.PublicKey shape escrow.EncryptForNode expects.
//
// Uses secp256k1.ParsePubKey rather than elliptic.Unmarshal —
// elliptic.Unmarshal has been deprecated since Go 1.21, and
// secp256k1.S256() is similarly deprecated by upstream in favor of
// the curve's specialized methods.
func parseRecipientPubKey(data []byte) (*ecdsa.PublicKey, error) {
	if len(data) == 0 {
		return nil, errors.New("empty public key bytes")
	}
	pub, err := secp256k1.ParsePubKey(data)
	if err != nil {
		return nil, fmt.Errorf("invalid secp256k1 public key (%d bytes): %w", len(data), err)
	}
	return pub.ToECDSA(), nil
}
