package keyservice

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/miekg/pkcs11"

	"github.com/clearcompass-ai/ortholog-sdk/lifecycle/artifact"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// PKCS11Config configures the PKCS#11-backed key service. ModulePath
// points at the module's shared-object file (e.g. SoftHSM2 ships
// /usr/lib/softhsm/libsofthsm2.so). TokenLabel selects the token by
// the label set during softhsm2-util --init-token; production HSMs
// pin a fixed slot.
//
// Pin is the CKU_USER PIN. KeyLabelPrefix is prepended to each
// per-artifact AES key's CKA_LABEL so multiple key services can
// share one token without colliding ("artifact-" by default).
type PKCS11Config struct {
	ModulePath     string
	TokenLabel     string
	Pin            string
	KeyLabelPrefix string
}

// PKCS11 implements artifact.ArtifactKeyService against a PKCS#11
// module (SoftHSM2 in dev/test, Thales/Equinix/Fortanix/AWS-CloudHSM/
// Azure-Managed-HSM in production — same code path).
//
// Per-artifact lifecycle:
//   - GenerateAndEncrypt: HSM generates persistent AES-256 key,
//     HSM AES-GCM encrypts plaintext (key never leaves HSM here),
//     CID computed over ciphertext, key labeled "artifact-<hex>".
//   - WrapForRecipient: HSM extracts key + nonce; ECIES-wrap to
//     recipient's secp256k1 pubkey; zeroize.
//   - Decrypt: HSM AES-GCM decrypts in-place, key never leaves HSM.
//   - Rotate: decrypt + re-encrypt under fresh HSM-resident key;
//     destroy old object → cryptographic erasure.
//   - Delete: destroy the persistent key object.
//
// TrustClass is ClassEnvelope: the recipient-wrap path momentarily
// holds the AES key in process memory to perform ECIES wrap (no HSM
// supports secp256k1 ECDH+CKM_AES_KEY_WRAP_PAD natively at the
// moment, including SoftHSM2 and the major cloud HSMs out of the
// box). The Decrypt path keeps the key fully inside the HSM.
//
// All operations serialize on a single mutex. PKCS#11 contexts are
// expensive to recreate, and the per-call session open/close pattern
// gives us isolation. Throughput on SoftHSM2 is ~1k ops/s per core,
// which is well above any realistic artifact-write rate.
type PKCS11 struct {
	cfg    PKCS11Config
	ctx    *pkcs11.Ctx
	slotID uint

	mu sync.Mutex
}

// NewPKCS11 loads the module, initializes the PKCS#11 context, and
// resolves the slot ID for cfg.TokenLabel. The token must exist —
// callers run softhsm2-util --init-token (or the vendor equivalent)
// before passing the label here.
func NewPKCS11(cfg PKCS11Config) (*PKCS11, error) {
	if cfg.ModulePath == "" {
		return nil, errors.New("keyservice/pkcs11: ModulePath is required")
	}
	if cfg.TokenLabel == "" {
		return nil, errors.New("keyservice/pkcs11: TokenLabel is required")
	}
	if cfg.Pin == "" {
		return nil, errors.New("keyservice/pkcs11: Pin is required")
	}
	if cfg.KeyLabelPrefix == "" {
		cfg.KeyLabelPrefix = "artifact-"
	}

	ctx := pkcs11.New(cfg.ModulePath)
	if ctx == nil {
		return nil, fmt.Errorf("keyservice/pkcs11: failed to load module %q", cfg.ModulePath)
	}
	if err := ctx.Initialize(); err != nil {
		ctx.Destroy()
		return nil, fmt.Errorf("keyservice/pkcs11: Initialize: %w", err)
	}
	slotID, err := findSlotByTokenLabel(ctx, cfg.TokenLabel)
	if err != nil {
		_ = ctx.Finalize()
		ctx.Destroy()
		return nil, err
	}
	return &PKCS11{cfg: cfg, ctx: ctx, slotID: slotID}, nil
}

// Close finalizes the PKCS#11 context. Safe to call once; redundant
// calls are no-ops.
func (p *PKCS11) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ctx == nil {
		return nil
	}
	_ = p.ctx.Finalize()
	p.ctx.Destroy()
	p.ctx = nil
	return nil
}

// TrustClass returns ClassEnvelope. See type-level docs.
func (p *PKCS11) TrustClass() artifact.TrustClass { return artifact.ClassEnvelope }

// keyLabel returns the per-artifact PKCS#11 object label. Format:
// "<prefix><sha256-hex>". Hex (not base64/base32) keeps the label
// printable in HSM admin UIs and well under PKCS#11's 256-byte
// CKA_LABEL bound.
func (p *PKCS11) keyLabel(cid storage.CID) string {
	return p.cfg.KeyLabelPrefix + hex.EncodeToString(cid.Digest)
}

// checkOpen returns an error if Close() has been called. Must be
// invoked with p.mu held.
func (p *PKCS11) checkOpen() error {
	if p.ctx == nil {
		return errors.New("keyservice/pkcs11: service is closed")
	}
	return nil
}

// Compile-time guard: PKCS11 satisfies ArtifactKeyService.
var _ artifact.ArtifactKeyService = (*PKCS11)(nil)
