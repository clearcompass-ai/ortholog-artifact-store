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

	IPFSGateway     string
	IPFSBearerToken string

	MirrorBackend     string
	MirrorEndpoint    string
	MirrorBucket      string
	MirrorBearerToken string
	MirrorMode        string

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

	// OperatorPublicKey is the Ed25519 public key of the operator that
	// signs upload tokens. Accepted encodings: PEM, hex (64 chars),
	// base64 (≈44 chars). Required when RequireUploadToken != "off".
	OperatorPublicKey     string
	OperatorPublicKeyFile string // alternative to OperatorPublicKey

	DefaultResolveExpiry time.Duration

	ListenAddr  string
	MaxBodySize int64
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
		IPFSGateway:          envOrDefault("ARTIFACT_IPFS_GATEWAY", "https://ipfs.io"),
		IPFSBearerToken:      os.Getenv("ARTIFACT_IPFS_BEARER_TOKEN"),
		MirrorBackend:        os.Getenv("ARTIFACT_MIRROR_BACKEND"),
		MirrorEndpoint:       os.Getenv("ARTIFACT_MIRROR_ENDPOINT"),
		MirrorBucket:         os.Getenv("ARTIFACT_MIRROR_BUCKET"),
		MirrorBearerToken:    os.Getenv("ARTIFACT_MIRROR_BEARER_TOKEN"),
		MirrorMode:           envOrDefault("ARTIFACT_MIRROR_MODE", "sync"),
		VerifyOnPush:          envBool("ARTIFACT_VERIFY_ON_PUSH", true),
		Env:                   envOrDefault("ORTHOLOG_ENV", "dev"),
		RequireUploadToken:    envOrDefault("ARTIFACT_REQUIRE_UPLOAD_TOKEN", "off"),
		OperatorPublicKey:     os.Getenv("ARTIFACT_OPERATOR_PUBKEY"),
		OperatorPublicKeyFile: os.Getenv("ARTIFACT_OPERATOR_PUBKEY_FILE"),
		DefaultResolveExpiry:  envDuration("ARTIFACT_RESOLVE_EXPIRY", 3600*time.Second),
		ListenAddr:           envOrDefault("ARTIFACT_LISTEN_ADDR", ":8082"),
		MaxBodySize:          envInt64("ARTIFACT_MAX_BODY_SIZE", 64<<20),
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	switch c.Backend {
	case "gcs", "s3", "ipfs", "memory":
	default:
		return fmt.Errorf("config: unknown backend %q (want gcs, s3, ipfs, or memory)", c.Backend)
	}
	if c.MirrorBackend != "" {
		switch c.MirrorBackend {
		case "gcs", "s3", "ipfs":
		default:
			return fmt.Errorf("config: unknown mirror backend %q", c.MirrorBackend)
		}
	}
	if c.MirrorMode != "sync" && c.MirrorMode != "async_pin" {
		return fmt.Errorf("config: unknown mirror mode %q (want sync or async_pin)", c.MirrorMode)
	}
	if c.MirrorMode == "async_pin" {
		if c.MirrorBackend != "ipfs" || c.Backend != "ipfs" {
			return fmt.Errorf("config: async_pin mode requires both primary and mirror to be ipfs")
		}
	}
	if c.MaxBodySize <= 0 {
		c.MaxBodySize = 64 << 20
	}
	switch c.RequireUploadToken {
	case "off", "optional", "required":
	default:
		return fmt.Errorf("config: unknown ARTIFACT_REQUIRE_UPLOAD_TOKEN %q (want off, optional, required)", c.RequireUploadToken)
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
