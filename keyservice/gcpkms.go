package keyservice

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	sdkartifact "github.com/clearcompass-ai/ortholog-sdk/crypto/artifact"
	"github.com/clearcompass-ai/ortholog-sdk/crypto/escrow"
	"github.com/clearcompass-ai/ortholog-sdk/lifecycle/artifact"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// GenerateAndEncrypt produces a fresh DEK in process, AES-GCM
// encrypts plaintext, wraps the (DEK||nonce) blob under the global
// KEK via Cloud KMS, and stores the wrapped blob in Firestore at
// the per-CID document ID.
//
// Failure model: KMS Encrypt fails BEFORE Firestore write — nothing
// to roll back (DEK was process-local). Firestore write fails AFTER
// KMS Encrypt — also nothing to roll back, since KMS Encrypt is
// stateless (no per-call state was created). This is structurally
// simpler than the Vault Transit case, which had to roll back the
// per-artifact KEK on kv-v2 failure.
func (g *GCPKMS) GenerateAndEncrypt(
	ctx context.Context, plaintext []byte,
) (storage.CID, []byte, error) {
	var keyBytes [sdkartifact.KeySize]byte
	if _, err := io.ReadFull(rand.Reader, keyBytes[:]); err != nil {
		return storage.CID{}, nil, fmt.Errorf("keyservice/gcpkms: rand: %w", err)
	}
	var nonceBytes [sdkartifact.NonceSize]byte
	if _, err := io.ReadFull(rand.Reader, nonceBytes[:]); err != nil {
		return storage.CID{}, nil, fmt.Errorf("keyservice/gcpkms: rand: %w", err)
	}

	ct, err := aesGCMSeal(keyBytes[:], nonceBytes[:], plaintext)
	if err != nil {
		zeroize(keyBytes[:])
		return storage.CID{}, nil, fmt.Errorf("keyservice/gcpkms: AES-GCM seal: %w", err)
	}
	cid := storage.Compute(ct)

	keyMaterial := append(append([]byte(nil), keyBytes[:]...), nonceBytes[:]...)
	wrapped, err := g.kmsEncrypt(ctx, keyMaterial)
	zeroize(keyMaterial)
	if err != nil {
		zeroize(keyBytes[:])
		return storage.CID{}, nil, err
	}
	if err := g.firestoreCreateWrapped(ctx, g.docID(cid), wrapped); err != nil {
		zeroize(keyBytes[:])
		return storage.CID{}, nil, err
	}
	zeroize(keyBytes[:])

	g.mu.Lock()
	g.pathCache[cid.String()] = struct{}{}
	g.mu.Unlock()
	return cid, ct, nil
}

// WrapForRecipient fetches the wrapped DEK from Firestore, asks
// Cloud KMS to decrypt it, ECIES-wraps the recovered (key||nonce)
// for the recipient's secp256k1 pubkey, and zeroizes.
func (g *GCPKMS) WrapForRecipient(
	ctx context.Context, cid storage.CID, recipientPub []byte,
) ([]byte, error) {
	if len(recipientPub) == 0 {
		return nil, artifact.ErrInvalidRecipientKey
	}
	pub, err := parseRecipientPubKey(recipientPub)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", artifact.ErrInvalidRecipientKey, err)
	}

	wrapped, err := g.firestoreGetWrapped(ctx, g.docID(cid))
	if err != nil {
		return nil, err
	}
	keyMaterial, err := g.kmsDecrypt(ctx, wrapped)
	if err != nil {
		return nil, err
	}
	defer zeroize(keyMaterial)

	envelope, err := escrow.EncryptForNode(keyMaterial, pub)
	if err != nil {
		return nil, fmt.Errorf("keyservice/gcpkms: ECIES wrap: %w", err)
	}
	return envelope, nil
}

// Decrypt fetches the wrapped DEK, KMS-decrypts to recover (key||
// nonce), then AES-GCM decrypts the artifact ciphertext in process.
func (g *GCPKMS) Decrypt(
	ctx context.Context, cid storage.CID, ciphertext []byte,
) ([]byte, error) {
	wrapped, err := g.firestoreGetWrapped(ctx, g.docID(cid))
	if err != nil {
		return nil, err
	}
	keyMaterial, err := g.kmsDecrypt(ctx, wrapped)
	if err != nil {
		return nil, err
	}
	defer zeroize(keyMaterial)
	if len(keyMaterial) != sdkartifact.KeySize+sdkartifact.NonceSize {
		return nil, fmt.Errorf("keyservice/gcpkms: unwrapped material length %d, want %d",
			len(keyMaterial), sdkartifact.KeySize+sdkartifact.NonceSize)
	}
	pt, err := aesGCMOpen(
		keyMaterial[:sdkartifact.KeySize],
		keyMaterial[sdkartifact.KeySize:],
		ciphertext,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", artifact.ErrCiphertextMismatch, err)
	}
	return pt, nil
}

// Rotate decrypts under the old wrapped-DEK record, re-encrypts
// with a fresh DEK, and deletes the old Firestore document.
// Cryptographic erasure: without the wrapped blob the KEK alone
// cannot reconstruct the prior DEK, so the old artifact ciphertext
// becomes opaque garbage.
func (g *GCPKMS) Rotate(
	ctx context.Context, oldCID storage.CID, oldCiphertext []byte,
) (storage.CID, []byte, error) {
	pt, err := g.Decrypt(ctx, oldCID, oldCiphertext)
	if err != nil {
		return storage.CID{}, nil, err
	}
	newCID, newCT, err := g.GenerateAndEncrypt(ctx, pt)
	zeroize(pt)
	if err != nil {
		return storage.CID{}, nil, err
	}
	if err := g.Delete(ctx, oldCID); err != nil {
		return newCID, newCT, fmt.Errorf("keyservice/gcpkms: post-rotate delete old: %w", err)
	}
	return newCID, newCT, nil
}

// Delete removes the per-CID Firestore document. Idempotent — a
// missing document is treated as success so callers can safely
// retry on partial failures.
func (g *GCPKMS) Delete(ctx context.Context, cid storage.CID) error {
	if err := g.firestoreDeleteWrapped(ctx, g.docID(cid)); err != nil {
		if !errors.Is(err, errGCPNotFound) {
			return err
		}
	}
	g.mu.Lock()
	delete(g.pathCache, cid.String())
	g.mu.Unlock()
	return nil
}
