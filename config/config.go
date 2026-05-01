/*
Package config loads artifact store configuration from environment variables.

All settings have sensible defaults. The backend selection
(ARTIFACT_BACKEND) is the only required setting for non-test deployments.
*/
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds all artifact store settings.
type Config struct {
	Backend   string
	Endpoint  string
	Bucket    string
	Region    string
	PathStyle bool
	Prefix    string

	MirrorBackend  string
	MirrorEndpoint string
	MirrorBucket   string
	MirrorMode     string

	VerifyOnPush bool

	// Env is the deployment environment, read from ORTHOLOG_ENV.
	// When Env == "production" and VerifyOnPush is false, main.go
	// starts a periodic re-warning goroutine so the misconfiguration
	// is impossible to miss across a long-running process.
	// Values are informational only — "production", "staging", "dev".
	Env string

	// RequireUploadToken is the X-Upload-Token policy:
	//   "off"      — no check; rely on network segmentation (default)
	//   "optional" — verify if present, accept if absent
	//   "required" — reject pushes without a valid token
	RequireUploadToken string

	// OperatorPubKeys is the kid-keyed Ed25519 public-key list of the
	// operators that sign upload tokens. Format:
	//   kid1:<encoded>,kid2:<encoded>
	// where <encoded> is one of PEM, hex (64 chars), or base64 (≈44
	// chars). The kid may be empty (":<encoded>") for single-key
	// deployments whose tokens omit the kid claim.
	//
	// Required when RequireUploadToken != "off" (unless
	// OperatorPubKeysDir is set instead).
	OperatorPubKeys string

	// OperatorPubKeysDir is an alternative to OperatorPubKeys: a
	// directory containing one PEM file per kid. Filename minus the
	// .pem extension is the kid. Mutually exclusive with
	// OperatorPubKeys.
	OperatorPubKeysDir string

	DefaultResolveExpiry time.Duration

	ListenAddr  string
	MaxBodySize int64

	// KeyService selects the artifact-key custody backend
	// (lifecycle/artifact.ArtifactKeyService implementation):
	//   "vault"   — HashiCorp Vault Transit OSS (default)
	//   "gcpkms"  — GCP Cloud KMS HSM + Firestore wrapped-DEK store
	//   "pkcs11"  — local PKCS#11 / SoftHSM2 / vendor HSM
	//   "memory"  — in-process reference impl (dev/test only)
	//
	// "memory" runs without external dependencies but provides no
	// persistence — keys vanish at process exit. The default is
	// "vault" because Vault Transit OSS is the cheapest production-
	// suitable backend ($0 self-hosted, ~$25/mo HCP Standard).
	KeyService string

	// Vault Transit settings — used when KeyService == "vault".
	// VaultEndpoint defaults to http://127.0.0.1:8200 to match
	// `vault server -dev` out of the box. VaultToken is required
	// when KeyService == "vault" (no auto-auth).
	VaultEndpoint     string
	VaultToken        string
	VaultTransitMount string
	VaultKVMount      string
	VaultKVNamespace  string

	// GCP KMS + Firestore settings — used when KeyService == "gcpkms".
	// GCPKMSKEKResource is the Cloud KMS resource path of the global
	// KEK (projects/.../cryptoKeys/<key>). GCPFirestoreProjectID is
	// the Firestore project; GCPFirestoreCollection scopes wrapped-
	// DEK documents. Auth uses Application Default Credentials (ADC):
	// the GOOGLE_APPLICATION_CREDENTIALS env var pointing at a
	// service-account JSON, or workload-identity in GKE/Cloud Run.
	// In dev mode the user can hit REAL Cloud KMS + Firestore by
	// setting these to a project they own — no fakes are wired
	// through the binary's startup path; fakes are test-only.
	GCPKMSKEKResource      string
	GCPFirestoreProjectID  string
	GCPFirestoreDatabase   string
	GCPFirestoreCollection string

	// PKCS#11 settings — used when KeyService == "pkcs11".
	// PKCS11ModulePath defaults to /usr/lib/softhsm/libsofthsm2.so
	// (Debian/Ubuntu canonical SoftHSM2 install path).
	PKCS11ModulePath string
	PKCS11TokenLabel string
	PKCS11Pin        string
}

// Load reads configuration from environment variables with sensible defaults.
func Load() (*Config, error) {
	cfg := &Config{
		Backend:              envOrDefault("ARTIFACT_BACKEND", "memory"),
		Endpoint:             os.Getenv("ARTIFACT_ENDPOINT"),
		Bucket:               envOrDefault("ARTIFACT_BUCKET", "ortholog-artifacts"),
		Region:               envOrDefault("ARTIFACT_REGION", "us-east-1"),
		PathStyle:            envBool("ARTIFACT_PATH_STYLE", false),
		Prefix:               os.Getenv("ARTIFACT_PREFIX"),
		MirrorBackend:        os.Getenv("ARTIFACT_MIRROR_BACKEND"),
		MirrorEndpoint:       os.Getenv("ARTIFACT_MIRROR_ENDPOINT"),
		MirrorBucket:         os.Getenv("ARTIFACT_MIRROR_BUCKET"),
		MirrorMode:           envOrDefault("ARTIFACT_MIRROR_MODE", "sync"),
		VerifyOnPush:         envBool("ARTIFACT_VERIFY_ON_PUSH", true),
		Env:                  envOrDefault("ORTHOLOG_ENV", "dev"),
		RequireUploadToken:   envOrDefault("ARTIFACT_REQUIRE_UPLOAD_TOKEN", "off"),
		OperatorPubKeys:      os.Getenv("ARTIFACT_OPERATOR_PUBKEYS"),
		OperatorPubKeysDir:   os.Getenv("ARTIFACT_OPERATOR_PUBKEYS_DIR"),
		DefaultResolveExpiry: envDuration("ARTIFACT_RESOLVE_EXPIRY", 3600*time.Second),
		ListenAddr:           envOrDefault("ARTIFACT_LISTEN_ADDR", ":8082"),
		MaxBodySize:          envInt64("ARTIFACT_MAX_BODY_SIZE", 64<<20),

		KeyService:        envOrDefault("ARTIFACT_KEYSERVICE", "vault"),
		VaultEndpoint:     envOrDefault("ARTIFACT_VAULT_ENDPOINT", "http://127.0.0.1:8200"),
		VaultToken:        os.Getenv("ARTIFACT_VAULT_TOKEN"),
		VaultTransitMount: envOrDefault("ARTIFACT_VAULT_TRANSIT_MOUNT", "transit"),
		VaultKVMount:      envOrDefault("ARTIFACT_VAULT_KV_MOUNT", "secret"),
		VaultKVNamespace:  envOrDefault("ARTIFACT_VAULT_KV_NAMESPACE", "artifact-keys"),

		GCPKMSKEKResource:      os.Getenv("ARTIFACT_GCP_KMS_KEK_RESOURCE"),
		GCPFirestoreProjectID:  os.Getenv("ARTIFACT_GCP_FIRESTORE_PROJECT_ID"),
		GCPFirestoreDatabase:   envOrDefault("ARTIFACT_GCP_FIRESTORE_DATABASE", "(default)"),
		GCPFirestoreCollection: envOrDefault("ARTIFACT_GCP_FIRESTORE_COLLECTION", "ortholog-artifact-keys"),

		PKCS11ModulePath: envOrDefault("ARTIFACT_PKCS11_MODULE_PATH", "/usr/lib/softhsm/libsofthsm2.so"),
		PKCS11TokenLabel: os.Getenv("ARTIFACT_PKCS11_TOKEN_LABEL"),
		PKCS11Pin:        os.Getenv("ARTIFACT_PKCS11_PIN"),
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	// Object-store backends only. The artifact store deliberately
	// does not support content-addressed networks like IPFS — bytes
	// in / bytes out / signed URLs out is the entire contract.
	switch c.Backend {
	case "gcs", "rustfs", "memory":
	default:
		return fmt.Errorf("config: unknown backend %q (want gcs, rustfs, or memory)", c.Backend)
	}
	if c.MirrorBackend != "" {
		switch c.MirrorBackend {
		case "gcs", "rustfs":
		default:
			return fmt.Errorf("config: unknown mirror backend %q (want gcs or rustfs)", c.MirrorBackend)
		}
	}
	// MirrorMode reserved for future expansion. The only supported
	// mode today is "sync" — synchronous double-write.
	if c.MirrorMode != "sync" {
		return fmt.Errorf("config: unknown mirror mode %q (want sync)", c.MirrorMode)
	}
	if c.MaxBodySize <= 0 {
		c.MaxBodySize = 64 << 20
	}
	switch c.RequireUploadToken {
	case "off", "optional", "required":
	default:
		return fmt.Errorf("config: unknown ARTIFACT_REQUIRE_UPLOAD_TOKEN %q (want off, optional, required)", c.RequireUploadToken)
	}

	// Keyservice selection + per-tier required-fields validation.
	// "memory" needs nothing; "vault"/"gcpkms"/"pkcs11" each have a
	// minimal set of values that must be present for the constructor
	// not to fail — surface the misconfiguration at startup, not at
	// first-write time.
	switch c.KeyService {
	case "memory":
	case "vault":
		if c.VaultToken == "" {
			return fmt.Errorf("config: ARTIFACT_VAULT_TOKEN is required when ARTIFACT_KEYSERVICE=vault")
		}
	case "gcpkms":
		if c.GCPKMSKEKResource == "" {
			return fmt.Errorf("config: ARTIFACT_GCP_KMS_KEK_RESOURCE is required when ARTIFACT_KEYSERVICE=gcpkms")
		}
		if c.GCPFirestoreProjectID == "" {
			return fmt.Errorf("config: ARTIFACT_GCP_FIRESTORE_PROJECT_ID is required when ARTIFACT_KEYSERVICE=gcpkms")
		}
	case "pkcs11":
		if c.PKCS11TokenLabel == "" {
			return fmt.Errorf("config: ARTIFACT_PKCS11_TOKEN_LABEL is required when ARTIFACT_KEYSERVICE=pkcs11")
		}
		if c.PKCS11Pin == "" {
			return fmt.Errorf("config: ARTIFACT_PKCS11_PIN is required when ARTIFACT_KEYSERVICE=pkcs11")
		}
	default:
		return fmt.Errorf("config: unknown ARTIFACT_KEYSERVICE %q (want vault, gcpkms, pkcs11, or memory)", c.KeyService)
	}
	return nil
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func envInt64(key string, fallback int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	secs, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fallback
	}
	return time.Duration(secs) * time.Second
}
