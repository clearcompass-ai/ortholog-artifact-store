package keyservice

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/clearcompass-ai/ortholog-sdk/lifecycle/artifact"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// GCPKMSConfig configures the GCP Cloud KMS + Firestore-backed key
// service.
//
// KEKResourceName is the full Cloud KMS resource name of the
// per-deployment KEK (single global key, NOT per-artifact). Format:
//
//	projects/<project-id>/locations/<location>/keyRings/<ring>/cryptoKeys/<key>
//
// The KEK is configured with purpose ENCRYPT_DECRYPT and the
// recommended algorithm GOOGLE_SYMMETRIC_ENCRYPTION (AES-256-GCM
// with KMS-internal metadata padding). HSM protection level
// gives FIPS 140-2 Level 3; SOFTWARE protection level is fine for
// dev/staging.
//
// FirestoreProjectID is the GCP project that hosts the Firestore
// database used for wrapped-DEK persistence. FirestoreDatabase is
// the database ID; "(default)" is the GCP convention for the only
// database in legacy projects. FirestoreCollection scopes wrapped
// DEK documents so multiple key services can share one project.
//
// KMSEndpoint and FirestoreEndpoint default to the public GCP
// endpoints. Tests override these with httptest.Server addresses.
//
// HTTPClient, when set, is used unauthenticated — the production
// path uses Application Default Credentials (ADC) via
// golang.org/x/oauth2/google to obtain a Bearer token. Tests
// supply a plain http.Client to bypass auth against the in-process
// fakes.
type GCPKMSConfig struct {
	KEKResourceName     string
	FirestoreProjectID  string
	FirestoreDatabase   string
	FirestoreCollection string

	KMSEndpoint       string
	FirestoreEndpoint string
	HTTPClient        *http.Client
}

// GCPKMS implements artifact.ArtifactKeyService against
// GCP Cloud KMS (envelope encryption) + Firestore (wrapped DEK
// persistence).
//
// Per-artifact lifecycle:
//   - GenerateAndEncrypt: generate DEK locally, AES-GCM encrypt
//     plaintext, wrap (DEK||nonce) under the global KEK via Cloud
//     KMS, store wrapped blob in Firestore at <collection>/<cid-hex>,
//     zeroize.
//   - WrapForRecipient: load wrapped from Firestore, KMS-decrypt to
//     get DEK||nonce, ECIES-wrap for recipient secp256k1 pubkey,
//     zeroize.
//   - Decrypt: load wrapped, KMS-decrypt, AES-GCM decrypt locally,
//     zeroize.
//   - Rotate: re-encrypt under fresh DEK + delete the old Firestore
//     document — cryptographic erasure of the prior version (KEK
//     alone cannot reproduce the old DEK without the wrapped blob).
//   - Delete: remove the Firestore document.
//
// TrustClass is ClassEnvelope. The DEK appears in process for AES-GCM
// ops + ECIES wrap; the KEK never leaves Cloud KMS HSM (HSM
// protection level) / TEE (SOFTWARE protection level).
type GCPKMS struct {
	cfg    GCPKMSConfig
	client *http.Client

	mu        sync.RWMutex
	pathCache map[string]struct{}
}

// NewGCPKMS constructs a GCP KMS + Firestore-backed key service. If
// cfg.HTTPClient is nil, the constructor wraps http.DefaultClient
// with an oauth2 token source from Application Default Credentials
// (ADC) — the same path the official Google SDKs use. Tests pass a
// plain http.Client so they can talk to httptest.Server fakes
// without auth.
func NewGCPKMS(ctx context.Context, cfg GCPKMSConfig) (*GCPKMS, error) {
	if cfg.KEKResourceName == "" {
		return nil, errors.New("keyservice/gcpkms: KEKResourceName is required")
	}
	if !strings.HasPrefix(cfg.KEKResourceName, "projects/") {
		return nil, errors.New("keyservice/gcpkms: KEKResourceName must be in projects/.../cryptoKeys/<name> form")
	}
	if cfg.FirestoreProjectID == "" {
		return nil, errors.New("keyservice/gcpkms: FirestoreProjectID is required")
	}
	if cfg.FirestoreCollection == "" {
		cfg.FirestoreCollection = "ortholog-artifact-keys"
	}
	if cfg.FirestoreDatabase == "" {
		cfg.FirestoreDatabase = "(default)"
	}
	if cfg.KMSEndpoint == "" {
		cfg.KMSEndpoint = "https://cloudkms.googleapis.com"
	}
	if cfg.FirestoreEndpoint == "" {
		cfg.FirestoreEndpoint = "https://firestore.googleapis.com"
	}
	cfg.KMSEndpoint = strings.TrimRight(cfg.KMSEndpoint, "/")
	cfg.FirestoreEndpoint = strings.TrimRight(cfg.FirestoreEndpoint, "/")

	client := cfg.HTTPClient
	if client == nil {
		ts, err := google.DefaultTokenSource(ctx,
			"https://www.googleapis.com/auth/cloud-platform",
			"https://www.googleapis.com/auth/datastore")
		if err != nil {
			return nil, fmt.Errorf("keyservice/gcpkms: ADC token source: %w", err)
		}
		client = oauth2.NewClient(ctx, ts)
	}

	return &GCPKMS{
		cfg:       cfg,
		client:    client,
		pathCache: make(map[string]struct{}),
	}, nil
}

// TrustClass returns ClassEnvelope. See type-level docs.
func (g *GCPKMS) TrustClass() artifact.TrustClass { return artifact.ClassEnvelope }

// firestoreDocPath returns the fully-qualified Firestore document
// path for the wrapped-DEK record keyed by cid. Used both for the
// REST URL and for the cache key.
func (g *GCPKMS) firestoreDocPath(cid storage.CID) string {
	return fmt.Sprintf("projects/%s/databases/%s/documents/%s/%s",
		g.cfg.FirestoreProjectID, g.cfg.FirestoreDatabase,
		g.cfg.FirestoreCollection, hex.EncodeToString(cid.Digest))
}

// firestoreParentPath returns the parent collection path used as
// the {parent} URL component for CreateDocument calls.
func (g *GCPKMS) firestoreParentPath() string {
	return fmt.Sprintf("projects/%s/databases/%s/documents/%s",
		g.cfg.FirestoreProjectID, g.cfg.FirestoreDatabase,
		g.cfg.FirestoreCollection)
}

// docID returns the Firestore document ID portion (CID hex).
func (g *GCPKMS) docID(cid storage.CID) string {
	return hex.EncodeToString(cid.Digest)
}

// Compile-time guard: GCPKMS satisfies ArtifactKeyService.
var _ artifact.ArtifactKeyService = (*GCPKMS)(nil)
