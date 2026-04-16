package signers

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTestServiceAccount generates a throwaway RSA keypair and writes a
// minimal service-account JSON to a temp file so LoadGCSServiceAccount can
// exercise its full parse path without needing a real GCP key in git.
func writeTestServiceAccount(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	pemBlock := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: pkcs8,
	})

	sa := map[string]string{
		"client_email": "[email protected]",
		"private_key":  string(pemBlock),
	}
	data, err := json.Marshal(sa)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	path := filepath.Join(t.TempDir(), "sa.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestLoadGCSServiceAccount_HappyPath(t *testing.T) {
	path := writeTestServiceAccount(t)
	signer, err := LoadGCSServiceAccount(path)
	if err != nil {
		t.Fatalf("LoadGCSServiceAccount: %v", err)
	}
	if signer == nil {
		t.Fatal("signer is nil")
	}
	if signer.email != "[email protected]" {
		t.Errorf("email: want test@..., got %s", signer.email)
	}
}

func TestLoadGCSServiceAccount_MissingFile(t *testing.T) {
	_, err := LoadGCSServiceAccount("/does/not/exist.json")
	if err == nil {
		t.Fatal("want error for missing file")
	}
}

func TestLoadGCSServiceAccount_InvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadGCSServiceAccount(path)
	if err == nil {
		t.Fatal("want error for non-JSON")
	}
}

func TestLoadGCSServiceAccount_MissingFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "incomplete.json")
	if err := os.WriteFile(path, []byte(`{"client_email":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadGCSServiceAccount(path)
	if err == nil {
		t.Fatal("want error for missing private_key")
	}
}

func TestLoadGCSServiceAccount_NonPEM(t *testing.T) {
	sa := map[string]string{
		"client_email": "[email protected]",
		"private_key":  "not pem formatted",
	}
	data, _ := json.Marshal(sa)
	path := filepath.Join(t.TempDir(), "badpem.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadGCSServiceAccount(path)
	if err == nil {
		t.Fatal("want error for non-PEM private_key")
	}
}

func TestGCSSigner_SignURL_Shape(t *testing.T) {
	path := writeTestServiceAccount(t)
	signer, err := LoadGCSServiceAccount(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	url, err := signer.SignURL("my-bucket", "path/to/artifact", 15*time.Minute)
	if err != nil {
		t.Fatalf("SignURL: %v", err)
	}

	for _, want := range []string{
		"https://storage.googleapis.com/my-bucket/path/to/artifact",
		"X-Goog-Algorithm=GOOG4-RSA-SHA256",
		"X-Goog-Credential=",
		"X-Goog-Date=",
		"X-Goog-Expires=900",
		"X-Goog-SignedHeaders=host",
		"X-Goog-Signature=",
	} {
		if !strings.Contains(url, want) {
			t.Errorf("signed URL missing %q\n  got: %s", want, url)
		}
	}
}
