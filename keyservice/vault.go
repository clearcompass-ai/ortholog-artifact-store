package keyservice

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	sdkartifact "github.com/clearcompass-ai/ortholog-sdk/crypto/artifact"
	"github.com/clearcompass-ai/ortholog-sdk/crypto/escrow"
	"github.com/clearcompass-ai/ortholog-sdk/lifecycle/artifact"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// VaultTransitConfig configures the Vault Transit-backed key service.
//
// Endpoint is the Vault server base URL (e.g., "http://127.0.0.1:8200").
// Token is a Vault token with this policy mounted on TransitMount and
// KVMount:
//
//	path "transit/keys/artifact/*"               { capabilities = ["create","read","update","delete"] }
//	path "transit/datakey/plaintext/artifact/*"  { capabilities = ["update"] }
//	path "transit/decrypt/artifact/*"            { capabilities = ["update"] }
//	path "transit/encrypt/artifact/*"            { capabilities = ["update"] }
//	path "secret/data/artifact-keys/*"           { capabilities = ["create","read","update","delete"] }
//	path "secret/metadata/artifact-keys/*"       { capabilities = ["delete"] }
//
// TransitMount and KVMount default to "transit" and "secret"
// respectively if zero. KVNamespace ("artifact-keys" by default)
// scopes the kv-v2 paths so multiple key-services can share one Vault.
type VaultTransitConfig struct {
	Endpoint     string
	Token        string
	TransitMount string
	KVMount      string
	KVNamespace  string
	HTTPClient   *http.Client
}

// VaultTransit implements artifact.ArtifactKeyService against
// HashiCorp Vault Transit OSS using envelope encryption.
//
// Per-artifact lifecycle:
//   - GenerateAndEncrypt: generate DEK locally, AES-GCM encrypt
//     plaintext, create per-artifact Transit KEK, encrypt DEK with
//     the KEK via Transit, store wrapped DEK in kv-v2, zeroize.
//   - WrapForRecipient: fetch wrapped DEK from kv-v2, decrypt via
//     Transit, ECIES wrap for recipient pubkey, zeroize.
//   - Decrypt: fetch wrapped DEK, decrypt via Transit, AES-GCM
//     decrypt ciphertext in process, zeroize.
//   - Rotate: re-encrypt under a fresh DEK + delete old per-artifact
//     KEK and kv-v2 entry → cryptographic erasure of old version.
//   - Delete: remove per-artifact KEK + kv-v2 entry.
//
// TrustClass is ClassEnvelope. The DEK appears in process memory
// briefly during operations; the KEK never appears in process memory.
type VaultTransit struct {
	cfg    VaultTransitConfig
	client *http.Client

	mu        sync.RWMutex
	pathCache map[string]struct{}
}

// NewVaultTransit constructs a Vault Transit-backed key service.
// Connectivity is not validated at construction time; the first
// operation surfaces unreachable-backend errors via
// artifact.ErrServiceUnavailable.
func NewVaultTransit(cfg VaultTransitConfig) (*VaultTransit, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("keyservice/vault: Endpoint is required")
	}
	if cfg.Token == "" {
		return nil, errors.New("keyservice/vault: Token is required")
	}
	if cfg.TransitMount == "" {
		cfg.TransitMount = "transit"
	}
	if cfg.KVMount == "" {
		cfg.KVMount = "secret"
	}
	if cfg.KVNamespace == "" {
		cfg.KVNamespace = "artifact-keys"
	}
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/")
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &VaultTransit{
		cfg:       cfg,
		client:    cfg.HTTPClient,
		pathCache: make(map[string]struct{}),
	}, nil
}

// TrustClass returns ClassEnvelope.
func (v *VaultTransit) TrustClass() artifact.TrustClass { return artifact.ClassEnvelope }

// transitKeyName returns the per-artifact Transit key name. Format:
// "artifact-<sha256-hex>" — Vault Transit's URL router interprets a
// slash in the key name as a path delimiter (returns 404 "unsupported
// path"), so we use a hyphen to keep Vault audit logs CID-aligned
// without breaking routing.
func (v *VaultTransit) transitKeyName(cid storage.CID) string {
	return "artifact-" + hex.EncodeToString(cid.Digest)
}

// GenerateAndEncrypt produces a fresh DEK in process, AES-GCM
// encrypts plaintext, wraps the DEK under a per-artifact Vault
// Transit key, and stores the wrapped DEK in kv-v2.
func (v *VaultTransit) GenerateAndEncrypt(
	ctx context.Context, plaintext []byte,
) (storage.CID, []byte, error) {
	var keyBytes [sdkartifact.KeySize]byte
	if _, err := io.ReadFull(rand.Reader, keyBytes[:]); err != nil {
		return storage.CID{}, nil, fmt.Errorf("keyservice/vault: rand: %w", err)
	}
	var nonceBytes [sdkartifact.NonceSize]byte
	if _, err := io.ReadFull(rand.Reader, nonceBytes[:]); err != nil {
		return storage.CID{}, nil, fmt.Errorf("keyservice/vault: rand: %w", err)
	}

	ct, err := aesGCMSeal(keyBytes[:], nonceBytes[:], plaintext)
	if err != nil {
		zeroize(keyBytes[:])
		return storage.CID{}, nil, fmt.Errorf("keyservice/vault: AES-GCM seal: %w", err)
	}
	cid := storage.Compute(ct)

	keyMaterial := append(append([]byte(nil), keyBytes[:]...), nonceBytes[:]...)
	keyName := v.transitKeyName(cid)
	if err := v.transitCreateKey(ctx, keyName); err != nil {
		zeroize(keyBytes[:])
		zeroize(keyMaterial)
		return storage.CID{}, nil, err
	}
	wrapped, err := v.transitEncrypt(ctx, keyName, keyMaterial)
	zeroize(keyMaterial)
	if err != nil {
		// Roll back the per-artifact Transit key we just created.
		// We return a zero CID on this path, so the caller has no
		// handle to clean it up; without this, every retry under a
		// sustained outage leaks a Transit key. Cleanup failures
		// are swallowed — the caller's primary error is more
		// useful than masking it with a rollback error.
		_ = v.transitDeleteKey(ctx, keyName)
		zeroize(keyBytes[:])
		return storage.CID{}, nil, err
	}
	if err := v.kvPutWrapped(ctx, cid, wrapped); err != nil {
		// Same rollback rationale as transitEncrypt failure: kv-v2
		// outage (mount permissions, storage backend full, etc.)
		// would otherwise accumulate one orphaned Transit key per
		// failed attempt because we return zero CID on this path.
		_ = v.transitDeleteKey(ctx, keyName)
		zeroize(keyBytes[:])
		return storage.CID{}, nil, err
	}
	zeroize(keyBytes[:])

	v.mu.Lock()
	v.pathCache[cid.String()] = struct{}{}
	v.mu.Unlock()
	return cid, ct, nil
}

// WrapForRecipient fetches the wrapped DEK, decrypts it via Transit,
// ECIES-wraps for the recipient's secp256k1 pubkey, and zeroizes.
func (v *VaultTransit) WrapForRecipient(
	ctx context.Context, cid storage.CID, recipientPub []byte,
) ([]byte, error) {
	if len(recipientPub) == 0 {
		return nil, artifact.ErrInvalidRecipientKey
	}
	pub, err := parseRecipientPubKey(recipientPub)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", artifact.ErrInvalidRecipientKey, err)
	}

	wrapped, err := v.kvGetWrapped(ctx, cid)
	if err != nil {
		return nil, err
	}
	keyMaterial, err := v.transitDecrypt(ctx, v.transitKeyName(cid), wrapped)
	if err != nil {
		return nil, err
	}
	defer zeroize(keyMaterial)

	envelope, err := escrow.EncryptForNode(keyMaterial, pub)
	if err != nil {
		return nil, fmt.Errorf("keyservice/vault: ECIES wrap: %w", err)
	}
	return envelope, nil
}

// Decrypt fetches the wrapped DEK, decrypts via Transit, then
// AES-GCM decrypts the artifact ciphertext in process.
func (v *VaultTransit) Decrypt(
	ctx context.Context, cid storage.CID, ciphertext []byte,
) ([]byte, error) {
	wrapped, err := v.kvGetWrapped(ctx, cid)
	if err != nil {
		return nil, err
	}
	keyMaterial, err := v.transitDecrypt(ctx, v.transitKeyName(cid), wrapped)
	if err != nil {
		return nil, err
	}
	defer zeroize(keyMaterial)
	if len(keyMaterial) != sdkartifact.KeySize+sdkartifact.NonceSize {
		return nil, fmt.Errorf("keyservice/vault: unwrapped material length %d, want %d",
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

// Rotate re-encrypts under a freshly-generated DEK and
// cryptographically erases the old per-artifact KEK and kv-v2 entry.
func (v *VaultTransit) Rotate(
	ctx context.Context, oldCID storage.CID, oldCiphertext []byte,
) (storage.CID, []byte, error) {
	pt, err := v.Decrypt(ctx, oldCID, oldCiphertext)
	if err != nil {
		return storage.CID{}, nil, err
	}
	newCID, newCT, err := v.GenerateAndEncrypt(ctx, pt)
	zeroize(pt)
	if err != nil {
		return storage.CID{}, nil, err
	}
	if err := v.Delete(ctx, oldCID); err != nil {
		return newCID, newCT, fmt.Errorf("keyservice/vault: post-rotate delete old: %w", err)
	}
	return newCID, newCT, nil
}

// Delete removes the per-artifact Transit key (cryptographic erasure
// of the KEK) and the kv-v2 wrapped-DEK entry. Idempotent.
func (v *VaultTransit) Delete(ctx context.Context, cid storage.CID) error {
	if err := v.kvDeleteMetadata(ctx, cid); err != nil {
		if !errors.Is(err, errVaultNotFound) {
			return err
		}
	}
	if err := v.transitDeleteKey(ctx, v.transitKeyName(cid)); err != nil {
		if !errors.Is(err, errVaultNotFound) {
			return err
		}
	}
	v.mu.Lock()
	delete(v.pathCache, cid.String())
	v.mu.Unlock()
	return nil
}

// Compile-time guard: VaultTransit satisfies ArtifactKeyService.
var _ artifact.ArtifactKeyService = (*VaultTransit)(nil)
