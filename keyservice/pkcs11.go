package keyservice

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/miekg/pkcs11"

	sdkartifact "github.com/clearcompass-ai/ortholog-sdk/crypto/artifact"
	"github.com/clearcompass-ai/ortholog-sdk/crypto/escrow"
	"github.com/clearcompass-ai/ortholog-sdk/lifecycle/artifact"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// GenerateAndEncrypt creates a fresh AES-256 key inside the HSM,
// encrypts plaintext with CKM_AES_GCM (key stays in HSM), commits
// the persistent object's CKA_LABEL to the CID, and returns the
// ciphertext. The DEK never leaves HSM memory on this path.
func (p *PKCS11) GenerateAndEncrypt(
	ctx context.Context, plaintext []byte,
) (storage.CID, []byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.checkOpen(); err != nil {
		return storage.CID{}, nil, err
	}

	var nonce [sdkartifact.NonceSize]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return storage.CID{}, nil, fmt.Errorf("keyservice/pkcs11: rand: %w", err)
	}

	s, err := p.openSession()
	if err != nil {
		return storage.CID{}, nil, err
	}
	defer s.Close()

	// Two-phase label commit: generate under a transient label,
	// encrypt, then SetAttributeValue the final CKA_LABEL once we
	// know the CID. This avoids the chicken-and-egg of needing the
	// ciphertext (which needs the key) before knowing the CID
	// (which needs the ciphertext).
	tmpLabel := p.cfg.KeyLabelPrefix + "tmp-" + hex.EncodeToString(nonce[:])
	oh, err := s.generateAESKey(tmpLabel, nonce[:])
	if err != nil {
		return storage.CID{}, nil, err
	}
	ct, err := s.gcmEncrypt(oh, nonce[:], plaintext)
	if err != nil {
		_ = s.destroyKey(oh)
		return storage.CID{}, nil, err
	}
	cid := storage.Compute(ct)
	finalLabel := p.keyLabel(cid)
	if err := p.ctx.SetAttributeValue(s.sh, oh,
		[]*pkcs11.Attribute{pkcs11.NewAttribute(pkcs11.CKA_LABEL, finalLabel)}); err != nil {
		_ = s.destroyKey(oh)
		return storage.CID{}, nil, fmt.Errorf("keyservice/pkcs11: SetAttributeValue(label): %w", err)
	}
	return cid, ct, nil
}

// WrapForRecipient extracts the AES key + nonce from the HSM, ECIES-
// wraps the concatenated key||nonce blob to the recipient's secp256k1
// pubkey, and zeroizes the in-process copy. Returns ErrKeyNotFound
// if cid has no matching HSM object.
func (p *PKCS11) WrapForRecipient(
	ctx context.Context, cid storage.CID, recipientPub []byte,
) ([]byte, error) {
	if len(recipientPub) == 0 {
		return nil, artifact.ErrInvalidRecipientKey
	}
	pub, err := parseRecipientPubKey(recipientPub)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", artifact.ErrInvalidRecipientKey, err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.checkOpen(); err != nil {
		return nil, err
	}

	s, err := p.openSession()
	if err != nil {
		return nil, err
	}
	defer s.Close()

	oh, err := s.findKeyByLabel(p.keyLabel(cid))
	if err != nil {
		if errors.Is(err, errPKCS11NotFound) {
			return nil, artifact.ErrKeyNotFound
		}
		return nil, err
	}
	key, nonce, err := s.readKeyMaterial(oh)
	if err != nil {
		return nil, err
	}
	keyMaterial := append(append([]byte(nil), key...), nonce...)
	zeroize(key)
	defer zeroize(keyMaterial)

	envelope, err := escrow.EncryptForNode(keyMaterial, pub)
	if err != nil {
		return nil, fmt.Errorf("keyservice/pkcs11: ECIES wrap: %w", err)
	}
	return envelope, nil
}

// Decrypt asks the HSM to AES-GCM-decrypt ciphertext under the
// per-artifact key. The DEK never enters process memory on this
// path. Returns ErrKeyNotFound if cid has no matching HSM object,
// ErrCiphertextMismatch if the GCM auth tag does not validate.
func (p *PKCS11) Decrypt(
	ctx context.Context, cid storage.CID, ciphertext []byte,
) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.checkOpen(); err != nil {
		return nil, err
	}

	s, err := p.openSession()
	if err != nil {
		return nil, err
	}
	defer s.Close()

	oh, err := s.findKeyByLabel(p.keyLabel(cid))
	if err != nil {
		if errors.Is(err, errPKCS11NotFound) {
			return nil, artifact.ErrKeyNotFound
		}
		return nil, err
	}
	// CKA_ID carries the GCM nonce we stored at generation time.
	attrs, err := p.ctx.GetAttributeValue(s.sh, oh,
		[]*pkcs11.Attribute{pkcs11.NewAttribute(pkcs11.CKA_ID, nil)})
	if err != nil {
		return nil, fmt.Errorf("keyservice/pkcs11: read CKA_ID: %w", err)
	}
	var nonce []byte
	for _, a := range attrs {
		if a.Type == pkcs11.CKA_ID {
			nonce = a.Value
		}
	}
	if len(nonce) != sdkartifact.NonceSize {
		return nil, fmt.Errorf("keyservice/pkcs11: stored nonce length %d, want %d",
			len(nonce), sdkartifact.NonceSize)
	}
	pt, err := s.gcmDecrypt(oh, nonce, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", artifact.ErrCiphertextMismatch, err)
	}
	return pt, nil
}

// Rotate decrypts under the old key, re-encrypts under a fresh HSM-
// resident key, and destroys the old key object. The destroyed key
// is unrecoverable from the HSM, so any wrapped DEK or ciphertext
// referring to the old CID becomes permanently undecryptable —
// cryptographic erasure of the prior version.
func (p *PKCS11) Rotate(
	ctx context.Context, oldCID storage.CID, oldCiphertext []byte,
) (storage.CID, []byte, error) {
	pt, err := p.Decrypt(ctx, oldCID, oldCiphertext)
	if err != nil {
		return storage.CID{}, nil, err
	}
	newCID, newCT, err := p.GenerateAndEncrypt(ctx, pt)
	zeroize(pt)
	if err != nil {
		return storage.CID{}, nil, err
	}
	if err := p.Delete(ctx, oldCID); err != nil {
		return newCID, newCT, fmt.Errorf("keyservice/pkcs11: post-rotate delete old: %w", err)
	}
	return newCID, newCT, nil
}

// Delete destroys the persistent HSM key object. Idempotent — a
// missing object is treated as success so callers can safely retry
// on partial failures.
func (p *PKCS11) Delete(ctx context.Context, cid storage.CID) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.checkOpen(); err != nil {
		return err
	}

	s, err := p.openSession()
	if err != nil {
		return err
	}
	defer s.Close()

	oh, err := s.findKeyByLabel(p.keyLabel(cid))
	if err != nil {
		if errors.Is(err, errPKCS11NotFound) {
			return nil
		}
		return err
	}
	if err := s.destroyKey(oh); err != nil {
		if errors.Is(err, errPKCS11NotFound) {
			return nil
		}
		return err
	}
	return nil
}
