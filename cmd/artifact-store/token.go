/*
cmd/artifact-store/token.go — kid-keyed upload-token wiring.

Loads the operator's Ed25519 public keys from config at startup and
constructs an api.UploadTokenVerifier. Returns nil (no verifier) when
ARTIFACT_REQUIRE_UPLOAD_TOKEN=off, which is the default — in the absence
of a policy decision, the store runs unauthenticated and the deployment
topology is responsible for preventing unauthorized access (typically
by running the store on a cluster-internal service address that only
the operator can reach).

Two key-loading paths, exclusive:

  ARTIFACT_OPERATOR_PUBKEYS
    Inline multi-key list:  kid1:<encoded>,kid2:<encoded>
    Each <encoded> is one of: PEM block, raw 64-char hex, or base64.
    A single-key deployment is just one entry. If the kid is empty
    (":<encoded>" with no kid), tokens that omit the kid claim match.

  ARTIFACT_OPERATOR_PUBKEYS_DIR
    Directory of PEM files. The kid is the filename minus the .pem
    extension. Useful for secret-mount workflows where each key is
    rotated/projected as its own file.

Operator key rotation is a kid-keyed swap:
  1. Operator mints tokens under a new kid.
  2. Artifact store loads BOTH old and new pubkeys (both env or both in
     the directory). Tokens under either kid verify cleanly.
  3. After in-flight tokens under the old kid expire, drop the old
     entry from the env / directory. Restart picks up the new map.
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
	"path/filepath"
	"sort"
	"strings"

	"github.com/clearcompass-ai/ortholog-artifact-store/api"
	"github.com/clearcompass-ai/ortholog-artifact-store/config"
)

// buildUploadTokenVerifier returns an api.UploadTokenVerifier configured
// with the operator's public keys, or nil when the policy is "off".
// Errors on misconfiguration (policy requires keys but none provided,
// or a key fails to parse).
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

	if cfg.OperatorPubKeys == "" && cfg.OperatorPubKeysDir == "" {
		return nil, fmt.Errorf(
			"config: ARTIFACT_REQUIRE_UPLOAD_TOKEN=%s requires ARTIFACT_OPERATOR_PUBKEYS "+
				"or ARTIFACT_OPERATOR_PUBKEYS_DIR", cfg.RequireUploadToken)
	}
	if cfg.OperatorPubKeys != "" && cfg.OperatorPubKeysDir != "" {
		return nil, fmt.Errorf(
			"config: ARTIFACT_OPERATOR_PUBKEYS and ARTIFACT_OPERATOR_PUBKEYS_DIR are " +
				"mutually exclusive; pick one")
	}

	var keys map[string]ed25519.PublicKey
	var err error
	if cfg.OperatorPubKeys != "" {
		keys, err = parseInlinePubKeys(cfg.OperatorPubKeys)
	} else {
		keys, err = loadPubKeysDir(cfg.OperatorPubKeysDir)
	}
	if err != nil {
		return nil, fmt.Errorf("config: operator pubkeys: %w", err)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("config: operator pubkeys: no keys loaded")
	}

	verifier, err := api.NewEd25519UploadTokenVerifier(keys)
	if err != nil {
		return nil, fmt.Errorf("config: build verifier: %w", err)
	}

	logger.Info("upload token: enabled",
		"event", "artifact.config.upload_token_enabled",
		"policy", cfg.RequireUploadToken,
		"operator_kids", sortedKids(keys),
	)
	return verifier, nil
}

// parseInlinePubKeys parses ARTIFACT_OPERATOR_PUBKEYS — a comma-
// separated list of "kid:<encoded>" entries. <encoded> is auto-
// detected as PEM, hex, or base64 (same heuristics used by the
// decode helpers below). The kid may be empty: ":<encoded>" registers
// under "" and matches tokens that omit the kid claim.
//
// Examples:
//
//	op-2026:-----BEGIN PUBLIC KEY-----...,op-2027:abc123...
//	op-prod-1:7e3a...64hexchars
//	:single-deployment-base64==
func parseInlinePubKeys(spec string) (map[string]ed25519.PublicKey, error) {
	out := make(map[string]ed25519.PublicKey)
	// Split on commas BUT preserve commas inside PEM blocks. Approach:
	// split greedily on ",<word>:" boundaries (a kid token followed by
	// a colon), which never appear inside a base64 / hex / PEM body
	// because PEM bodies don't carry colons after the BEGIN line and
	// hex/base64 don't carry them at all.
	entries := splitInlineEntries(spec)
	for i, entry := range entries {
		colon := strings.IndexByte(entry, ':')
		if colon < 0 {
			return nil, fmt.Errorf("entry %d: missing kid:value separator (':')", i)
		}
		kid := strings.TrimSpace(entry[:colon])
		encoded := strings.TrimSpace(entry[colon+1:])
		if encoded == "" {
			return nil, fmt.Errorf("entry %d (kid=%q): empty key value", i, kid)
		}
		key, err := decodePubKey(encoded)
		if err != nil {
			return nil, fmt.Errorf("entry %d (kid=%q): %w", i, kid, err)
		}
		if _, exists := out[kid]; exists {
			return nil, fmt.Errorf("entry %d (kid=%q): duplicate kid", i, kid)
		}
		out[kid] = key
	}
	return out, nil
}

// splitInlineEntries splits the inline-keys spec on top-level commas.
// A comma inside a PEM body is preserved by recognising the
// "-----BEGIN" and "-----END" delimiters and only splitting outside
// PEM blocks. Hex and base64 encodings never contain commas, so the
// non-PEM split just falls back to standard comma separation.
func splitInlineEntries(spec string) []string {
	if !strings.Contains(spec, "-----BEGIN") {
		// Fast path: hex / base64 only — comma splits cleanly.
		out := strings.Split(spec, ",")
		// Trim empty tail (allows trailing comma in env).
		filtered := out[:0]
		for _, e := range out {
			if strings.TrimSpace(e) != "" {
				filtered = append(filtered, e)
			}
		}
		return filtered
	}
	// PEM-aware split: walk character-by-character, only emitting at
	// commas that sit OUTSIDE -----BEGIN/-----END pairs.
	var out []string
	var current strings.Builder
	depth := 0
	for i := 0; i < len(spec); i++ {
		if depth == 0 && spec[i] == ',' {
			s := strings.TrimSpace(current.String())
			if s != "" {
				out = append(out, s)
			}
			current.Reset()
			continue
		}
		if i+11 <= len(spec) && spec[i:i+11] == "-----BEGIN " {
			depth++
		}
		if i+9 <= len(spec) && spec[i:i+9] == "-----END " {
			// ----END consumes through to the matching -----, then
			// closes the depth on the trailing 5 dashes.
			depth--
		}
		current.WriteByte(spec[i])
	}
	if s := strings.TrimSpace(current.String()); s != "" {
		out = append(out, s)
	}
	return out
}

// loadPubKeysDir loads every *.pem file from the directory. The kid is
// the filename minus the .pem extension. Each file MUST contain
// exactly one PEM-encoded Ed25519 public key.
func loadPubKeysDir(dir string) (map[string]ed25519.PublicKey, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read dir %q: %w", dir, err)
	}
	out := make(map[string]ed25519.PublicKey)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".pem") {
			continue
		}
		kid := strings.TrimSuffix(name, ".pem")
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %q: %w", path, err)
		}
		key, err := decodePubKey(strings.TrimSpace(string(raw)))
		if err != nil {
			return nil, fmt.Errorf("parse %q (kid=%q): %w", path, kid, err)
		}
		if _, exists := out[kid]; exists {
			return nil, fmt.Errorf("duplicate kid %q in dir %q", kid, dir)
		}
		out[kid] = key
	}
	return out, nil
}

// decodePubKey parses a single Ed25519 public key from PEM, hex, or
// base64. Returns the 32-byte key bytes.
func decodePubKey(raw string) (ed25519.PublicKey, error) {
	raw = strings.TrimSpace(raw)
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
			return ed25519.PublicKey(block.Bytes), nil
		}
		if len(block.Bytes) >= ed25519.PublicKeySize {
			return ed25519.PublicKey(block.Bytes[len(block.Bytes)-ed25519.PublicKeySize:]), nil
		}
		return nil, fmt.Errorf("PEM key too short: %d bytes", len(block.Bytes))
	}

	// Hex: exactly 64 chars, all hex.
	if len(raw) == 2*ed25519.PublicKeySize {
		if b, err := hex.DecodeString(raw); err == nil {
			return ed25519.PublicKey(b), nil
		}
	}

	// Base64.
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("neither hex nor base64: %w", err)
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("base64-decoded key is %d bytes, want %d", len(b), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(b), nil
}

// sortedKids returns the kids in deterministic order for the startup
// log line. Helps operators eyeball which kids are loaded.
func sortedKids(keys map[string]ed25519.PublicKey) []string {
	out := make([]string, 0, len(keys))
	for k := range keys {
		if k == "" {
			out = append(out, "<empty>")
		} else {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
