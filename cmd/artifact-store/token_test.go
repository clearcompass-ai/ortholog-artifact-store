package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
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

func TestBuildUploadTokenVerifier_MissingKey_Errors(t *testing.T) {
	for _, policy := range []string{"optional", "required"} {
		policy := policy
		t.Run(policy, func(t *testing.T) {
			cfg := &config.Config{RequireUploadToken: policy}
			v, err := buildUploadTokenVerifier(cfg, slog.Default())
			if err == nil {
				t.Fatalf("want error for policy=%s with no key, got verifier=%v", policy, v)
			}
			if !strings.Contains(err.Error(), "ARTIFACT_OPERATOR_PUBKEY") {
				t.Fatalf("error should mention the pubkey env vars, got: %v", err)
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

// ─── Key loading: accepted encodings ─────────────────────────────────

func TestLoadPublicKey_Base64Inline(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	cfg := &config.Config{
		RequireUploadToken: "required",
		OperatorPublicKey:  base64.StdEncoding.EncodeToString(pub),
	}
	got, err := loadPublicKey(cfg)
	if err != nil {
		t.Fatalf("loadPublicKey: %v", err)
	}
	if !equalBytes(got, pub) {
		t.Fatalf("bytes mismatch: got %x, want %x", got, pub)
	}
}

func TestLoadPublicKey_HexInline(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	cfg := &config.Config{
		RequireUploadToken: "required",
		OperatorPublicKey:  hex.EncodeToString(pub),
	}
	got, err := loadPublicKey(cfg)
	if err != nil {
		t.Fatalf("loadPublicKey: %v", err)
	}
	if !equalBytes(got, pub) {
		t.Fatalf("bytes mismatch")
	}
}

func TestLoadPublicKey_FromFile(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	path := filepath.Join(dir, "op.pub")
	// Write base64 (the most common deployment encoding).
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(pub)), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg := &config.Config{
		RequireUploadToken:    "required",
		OperatorPublicKeyFile: path,
	}
	got, err := loadPublicKey(cfg)
	if err != nil {
		t.Fatalf("loadPublicKey: %v", err)
	}
	if !equalBytes(got, pub) {
		t.Fatalf("bytes mismatch")
	}
}

func TestBuildUploadTokenVerifier_HappyPath(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	cfg := &config.Config{
		RequireUploadToken: "required",
		OperatorPublicKey:  base64.StdEncoding.EncodeToString(pub),
	}
	v, err := buildUploadTokenVerifier(cfg, slog.Default())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if v == nil {
		t.Fatal("want verifier, got nil")
	}
}

func TestBuildUploadTokenVerifier_WrongKeyLength_Errors(t *testing.T) {
	// Not 32 bytes — must be rejected before the verifier is returned.
	cfg := &config.Config{
		RequireUploadToken: "required",
		OperatorPublicKey:  base64.StdEncoding.EncodeToString([]byte("short")),
	}
	_, err := buildUploadTokenVerifier(cfg, slog.Default())
	if err == nil {
		t.Fatal("short key should error")
	}
	if !strings.Contains(err.Error(), "ed25519") {
		t.Fatalf("error should mention ed25519, got: %v", err)
	}
}

// tiny helper local to this file to avoid depending on other packages.
func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
