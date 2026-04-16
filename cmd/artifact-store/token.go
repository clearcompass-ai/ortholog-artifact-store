/*
cmd/artifact-store/token.go — upload token wiring.

Loads the operator's Ed25519 public key from config at startup and
constructs an api.UploadTokenVerifier. Returns nil (no verifier) when
ARTIFACT_REQUIRE_UPLOAD_TOKEN=off, which is the default — in the absence
of a policy decision, the store runs unauthenticated and the deployment
topology is responsible for preventing unauthorized access (typically
by running the store on a cluster-internal service address that only
the operator can reach).
*/
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/clearcompass-ai/ortholog-artifact-store/api"
	"github.com/clearcompass-ai/ortholog-artifact-store/config"
)

// buildUploadTokenVerifier returns an api.UploadTokenVerifier configured
// with the operator's public key, or nil when the policy is "off".
// Errors on misconfiguration (policy requires a key but none provided,
// or the key fails to parse).
func buildUploadTokenVerifier(cfg *config.Config, logger *slog.Logger) (api.UploadTokenVerifier, error) {
	switch cfg.RequireUploadToken {
	case "off":
		logger.Info("upload token: disabled (relying on network segmentation)",
			"event", "artifact.config.upload_token_off")
		return nil, nil
	case "optional", "required":
		// fallthrough into key loading
	default:
		return nil, fmt.Errorf(
			"config: ARTIFACT_REQUIRE_UPLOAD_TOKEN=%q (want off|optional|required)",
			cfg.RequireUploadToken)
	}

	if cfg.OperatorPublicKey == "" && cfg.OperatorPublicKeyFile == "" {
		return nil, fmt.Errorf(
			"config: ARTIFACT_REQUIRE_UPLOAD_TOKEN=%s requires ARTIFACT_OPERATOR_PUBKEY "+
				"or ARTIFACT_OPERATOR_PUBKEY_FILE", cfg.RequireUploadToken)
	}

	keyBytes, err := loadPublicKey(cfg)
	if err != nil {
		return nil, fmt.Errorf("config: operator public key: %w", err)
	}
	if len(keyBytes) != ed25519.PublicKeySize {
		return nil, fmt.Errorf(
			"config: operator public key: want %d bytes (ed25519), got %d",
			ed25519.PublicKeySize, len(keyBytes))
	}

	logger.Info("upload token: enabled",
		"event", "artifact.config.upload_token_enabled",
		"policy", cfg.RequireUploadToken,
	)

	return api.NewEd25519UploadTokenVerifier(ed25519.PublicKey(keyBytes)), nil
}

// loadPublicKey resolves the operator public key from either the inline
// value (ARTIFACT_OPERATOR_PUBKEY) or the file path
// (ARTIFACT_OPERATOR_PUBKEY_FILE). Inline wins if both are set.
//
// Supported encodings (auto-detected):
//   - PEM-wrapped Ed25519 public key (----BEGIN PUBLIC KEY----)
//   - Raw base64 (32-byte key → ~44 chars)
//   - Raw hex (64 chars)
func loadPublicKey(cfg *config.Config) ([]byte, error) {
	raw := strings.TrimSpace(cfg.OperatorPublicKey)
	if raw == "" {
		b, err := os.ReadFile(cfg.OperatorPublicKeyFile)
		if err != nil {
			return nil, fmt.Errorf("read %q: %w", cfg.OperatorPublicKeyFile, err)
		}
		raw = strings.TrimSpace(string(b))
	}
	if raw == "" {
		return nil, fmt.Errorf("key value is empty")
	}

	// PEM.
	if strings.HasPrefix(raw, "-----BEGIN") {
		block, _ := pem.Decode([]byte(raw))
		if block == nil {
			return nil, fmt.Errorf("PEM decode failed")
		}
		// For Ed25519, a bare-key PEM carries the raw 32 bytes.
		// For X.509-wrapped SubjectPublicKeyInfo the key is at the end.
		if len(block.Bytes) == ed25519.PublicKeySize {
			return block.Bytes, nil
		}
		if len(block.Bytes) >= ed25519.PublicKeySize {
			return block.Bytes[len(block.Bytes)-ed25519.PublicKeySize:], nil
		}
		return nil, fmt.Errorf("PEM key too short: %d bytes", len(block.Bytes))
	}

	// Hex: exactly 64 chars, all hex.
	if len(raw) == 2*ed25519.PublicKeySize {
		if b, err := hex.DecodeString(raw); err == nil {
			return b, nil
		}
	}

	// Base64.
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("neither hex nor base64: %w", err)
	}
	return b, nil
}
