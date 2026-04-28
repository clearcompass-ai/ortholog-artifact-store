package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/clearcompass-ai/ortholog-artifact-store/config"
)

// ─── buildUploadTokenVerifier: policy decisions ──────────────────────

func TestBuildUploadTokenVerifier_PolicyOff_ReturnsNil(t *testing.T) {
	cfg := &config.Config{RequireUploadToken: "off"}
	v, err := buildUploadTokenVerifier(cfg, slog.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != nil {
		t.Fatalf("policy=off must return nil verifier, got %T", v)
	}
}

func TestBuildUploadTokenVerifier_MissingKeys_Errors(t *testing.T) {
	for _, policy := range []string{"optional", "required"} {
		policy := policy
		t.Run(policy, func(t *testing.T) {
			cfg := &config.Config{RequireUploadToken: policy}
			v, err := buildUploadTokenVerifier(cfg, slog.Default())
			if err == nil {
				t.Fatalf("want error for policy=%s with no keys, got verifier=%v", policy, v)
			}
			if !strings.Contains(err.Error(), "ARTIFACT_OPERATOR_PUBKEYS") {
				t.Fatalf("error should mention the pubkeys env vars, got: %v", err)
			}
		})
	}
}

func TestBuildUploadTokenVerifier_UnknownPolicy_Errors(t *testing.T) {
	cfg := &config.Config{RequireUploadToken: "maybe-sometimes"}
	_, err := buildUploadTokenVerifier(cfg, slog.Default())
	if err == nil {
		t.Fatal("unknown policy must error")
	}
}

func TestBuildUploadTokenVerifier_BothInlineAndDir_Errors(t *testing.T) {
	cfg := &config.Config{
		RequireUploadToken: "required",
		OperatorPubKeys:    "kid:" + hexEncodedKey(t),
		OperatorPubKeysDir: t.TempDir(),
	}
	_, err := buildUploadTokenVerifier(cfg, slog.Default())
	if err == nil {
		t.Fatal("inline + dir together must error")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("want 'mutually exclusive' in err, got: %v", err)
	}
}

// ─── ARTIFACT_OPERATOR_PUBKEYS inline parsing ────────────────────────

func TestBuildUploadTokenVerifier_InlineSingleKey_HexEmptyKid(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	cfg := &config.Config{
		RequireUploadToken: "required",
		// kid="" — single-key deployment style.
		OperatorPubKeys: ":" + hex.EncodeToString(pub),
	}
	v, err := buildUploadTokenVerifier(cfg, slog.Default())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if v == nil {
		t.Fatal("want verifier, got nil")
	}
}

func TestBuildUploadTokenVerifier_InlineSingleKey_Base64WithKid(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	cfg := &config.Config{
		RequireUploadToken: "required",
		OperatorPubKeys:    "op-2027:" + base64.StdEncoding.EncodeToString(pub),
	}
	v, err := buildUploadTokenVerifier(cfg, slog.Default())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if v == nil {
		t.Fatal("want verifier, got nil")
	}
}

func TestBuildUploadTokenVerifier_InlineMultiKey_HexAndBase64(t *testing.T) {
	pub1, _, _ := ed25519.GenerateKey(rand.Reader)
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	cfg := &config.Config{
		RequireUploadToken: "required",
		OperatorPubKeys: fmt.Sprintf("op-2026:%s,op-2027:%s",
			hex.EncodeToString(pub1),
			base64.StdEncoding.EncodeToString(pub2)),
	}
	v, err := buildUploadTokenVerifier(cfg, slog.Default())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if v == nil {
		t.Fatal("want verifier, got nil")
	}
}

func TestBuildUploadTokenVerifier_InlinePEMMultiKey(t *testing.T) {
	// PEM bodies contain commas inside the base64 wrapping is unusual,
	// but the splitter must not break PEM blocks. Two PEM-encoded keys
	// separated by a comma at top level.
	pub1, _, _ := ed25519.GenerateKey(rand.Reader)
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	pem1 := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub1}))
	pem2 := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub2}))
	cfg := &config.Config{
		RequireUploadToken: "required",
		OperatorPubKeys:    fmt.Sprintf("op-old:%s,op-new:%s", pem1, pem2),
	}
	v, err := buildUploadTokenVerifier(cfg, slog.Default())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if v == nil {
		t.Fatal("want verifier, got nil")
	}
}

func TestBuildUploadTokenVerifier_InlineDuplicateKid_Errors(t *testing.T) {
	pub1, _, _ := ed25519.GenerateKey(rand.Reader)
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	cfg := &config.Config{
		RequireUploadToken: "required",
		OperatorPubKeys: fmt.Sprintf("op-x:%s,op-x:%s",
			hex.EncodeToString(pub1), hex.EncodeToString(pub2)),
	}
	_, err := buildUploadTokenVerifier(cfg, slog.Default())
	if err == nil {
		t.Fatal("duplicate kid must error")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("want 'duplicate' in err, got: %v", err)
	}
}

func TestBuildUploadTokenVerifier_InlineMissingColon_Errors(t *testing.T) {
	cfg := &config.Config{
		RequireUploadToken: "required",
		OperatorPubKeys:    hex.EncodeToString(make([]byte, 32)), // no kid: prefix
	}
	_, err := buildUploadTokenVerifier(cfg, slog.Default())
	if err == nil {
		t.Fatal("missing colon must error")
	}
}

func TestBuildUploadTokenVerifier_InlineWrongKeyLength_Errors(t *testing.T) {
	cfg := &config.Config{
		RequireUploadToken: "required",
		OperatorPubKeys:    "op-short:" + base64.StdEncoding.EncodeToString([]byte("short")),
	}
	_, err := buildUploadTokenVerifier(cfg, slog.Default())
	if err == nil {
		t.Fatal("short key must error")
	}
	if !strings.Contains(err.Error(), "ed25519") && !strings.Contains(err.Error(), "32") {
		t.Fatalf("error should mention ed25519 length, got: %v", err)
	}
}

// ─── ARTIFACT_OPERATOR_PUBKEYS_DIR loading ───────────────────────────

func TestBuildUploadTokenVerifier_DirLoadsAllPemFiles(t *testing.T) {
	dir := t.TempDir()
	pub1, _, _ := ed25519.GenerateKey(rand.Reader)
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	writePEM(t, dir, "op-2026.pem", pub1)
	writePEM(t, dir, "op-2027.pem", pub2)
	// A non-pem file MUST be ignored, not break the loader.
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("ignored"), 0o600); err != nil {
		t.Fatalf("WriteFile README: %v", err)
	}

	cfg := &config.Config{
		RequireUploadToken: "required",
		OperatorPubKeysDir: dir,
	}
	v, err := buildUploadTokenVerifier(cfg, slog.Default())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if v == nil {
		t.Fatal("want verifier, got nil")
	}
}

func TestBuildUploadTokenVerifier_DirEmpty_Errors(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		RequireUploadToken: "required",
		OperatorPubKeysDir: dir,
	}
	_, err := buildUploadTokenVerifier(cfg, slog.Default())
	if err == nil {
		t.Fatal("empty dir must error (no keys loaded)")
	}
}

// ─── helpers ─────────────────────────────────────────────────────────

func writePEM(t *testing.T, dir, name string, pub ed25519.PublicKey) {
	t.Helper()
	body := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub})
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("WriteFile %q: %v", path, err)
	}
}

func hexEncodedKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return hex.EncodeToString(pub)
}
