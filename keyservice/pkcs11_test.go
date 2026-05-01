package keyservice

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/clearcompass-ai/ortholog-sdk/lifecycle/artifact"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// softhsm2Module is the canonical install path on Debian/Ubuntu.
// Override with SOFTHSM2_MODULE if your distro places it elsewhere.
const softhsm2Module = "/usr/lib/softhsm/libsofthsm2.so"

// softhsm2ModulePath resolves the SoftHSM2 .so path, honoring an
// override env var. Tests that depend on it skip cleanly when the
// module is absent (e.g., dev environment without softhsm2 installed).
func softhsm2ModulePath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("SOFTHSM2_MODULE"); p != "" {
		return p
	}
	return softhsm2Module
}

// softhsm2Setup stands up a per-test SoftHSM2 token in a temp dir.
// Each test gets its own SOFTHSM2_CONF and tokendir, so test runs
// remain hermetic — no cross-test interference.
//
// Returns the module path, the token label we initialized, and the
// CKU_USER PIN. The temp tree is cleaned up via t.Cleanup.
func softhsm2Setup(t *testing.T) (modulePath, tokenLabel, pin string) {
	t.Helper()
	mod := softhsm2ModulePath(t)
	if _, err := os.Stat(mod); err != nil {
		t.Skipf("softhsm2 module not found at %s: %v (set SOFTHSM2_MODULE to override)", mod, err)
	}
	if _, err := exec.LookPath("softhsm2-util"); err != nil {
		t.Skipf("softhsm2-util not on PATH: %v", err)
	}

	tokenDir := t.TempDir()
	confPath := filepath.Join(tokenDir, "softhsm2.conf")
	conf := fmt.Sprintf("directories.tokendir = %s\nobjectstore.backend = file\nlog.level = ERROR\n", tokenDir)
	if err := os.WriteFile(confPath, []byte(conf), 0o600); err != nil {
		t.Fatalf("write softhsm2.conf: %v", err)
	}
	t.Setenv("SOFTHSM2_CONF", confPath)

	tokenLabel = "ortholog-test"
	pin = "1234"
	soPin := "5678"
	cmd := exec.Command("softhsm2-util",
		"--init-token", "--free",
		"--label", tokenLabel,
		"--pin", pin,
		"--so-pin", soPin)
	cmd.Env = append(os.Environ(), "SOFTHSM2_CONF="+confPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("softhsm2-util init-token: %v\n%s", err, out)
	}
	return mod, tokenLabel, pin
}

// newPKCS11ForTest builds the service and registers a Cleanup that
// closes it. Tests don't need their own t.Cleanup wiring.
func newPKCS11ForTest(t *testing.T) *PKCS11 {
	t.Helper()
	mod, label, pin := softhsm2Setup(t)
	svc, err := NewPKCS11(PKCS11Config{
		ModulePath: mod,
		TokenLabel: label,
		Pin:        pin,
	})
	if err != nil {
		t.Fatalf("NewPKCS11: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

// TestPKCS11_Conformance runs the SDK's shared conformance suite
// against a real SoftHSM2 token. This is the contract test — every
// PKCS#11 backend ships when this passes.
func TestPKCS11_Conformance(t *testing.T) {
	svc := newPKCS11ForTest(t)
	artifact.RunConformance(t, svc)
}

// TestPKCS11_TrustClass pins the trust-class declaration.
// SoftHSM2 (and the recipient-wrap path on every other PKCS#11
// module we'd realistically deploy with) has the AES key extractable
// in process memory during WrapForRecipient — that's ClassEnvelope.
func TestPKCS11_TrustClass(t *testing.T) {
	svc := newPKCS11ForTest(t)
	if svc.TrustClass() != artifact.ClassEnvelope {
		t.Errorf("TrustClass = %v, want ClassEnvelope", svc.TrustClass())
	}
}

// TestNewPKCS11_RejectsMissingModulePath locks the constructor
// validation contract — empty ModulePath must produce a clear
// configuration error before any HSM I/O.
func TestNewPKCS11_RejectsMissingModulePath(t *testing.T) {
	_, err := NewPKCS11(PKCS11Config{TokenLabel: "x", Pin: "1"})
	if err == nil {
		t.Fatal("expected error for missing ModulePath, got nil")
	}
}

// TestNewPKCS11_RejectsMissingTokenLabel locks the constructor
// validation contract.
func TestNewPKCS11_RejectsMissingTokenLabel(t *testing.T) {
	_, err := NewPKCS11(PKCS11Config{ModulePath: "x", Pin: "1"})
	if err == nil {
		t.Fatal("expected error for missing TokenLabel, got nil")
	}
}

// TestNewPKCS11_RejectsMissingPin locks the constructor validation
// contract.
func TestNewPKCS11_RejectsMissingPin(t *testing.T) {
	_, err := NewPKCS11(PKCS11Config{ModulePath: "x", TokenLabel: "y"})
	if err == nil {
		t.Fatal("expected error for missing Pin, got nil")
	}
}

// TestNewPKCS11_RejectsBadModule asserts that a non-existent .so
// path produces a load error rather than a panic. The miekg binding
// returns nil from New() when dlopen fails; we translate that.
func TestNewPKCS11_RejectsBadModule(t *testing.T) {
	_, err := NewPKCS11(PKCS11Config{
		ModulePath: filepath.Join(t.TempDir(), "does-not-exist.so"),
		TokenLabel: "x",
		Pin:        "1",
	})
	if err == nil {
		t.Fatal("expected error for non-existent module path, got nil")
	}
}

// TestNewPKCS11_RejectsUnknownToken asserts that pointing at a real
// module but a non-existent token label fails with a clear message
// (no slot found) rather than a panic on first operation.
func TestNewPKCS11_RejectsUnknownToken(t *testing.T) {
	mod := softhsm2ModulePath(t)
	if _, err := os.Stat(mod); err != nil {
		t.Skipf("softhsm2 module not found: %v", err)
	}
	tokenDir := t.TempDir()
	conf := filepath.Join(tokenDir, "softhsm2.conf")
	_ = os.WriteFile(conf,
		[]byte(fmt.Sprintf("directories.tokendir = %s\n", tokenDir)), 0o600)
	t.Setenv("SOFTHSM2_CONF", conf)

	_, err := NewPKCS11(PKCS11Config{
		ModulePath: mod,
		TokenLabel: "no-such-token-label-xyz",
		Pin:        "1",
	})
	if err == nil {
		t.Fatal("expected error for unknown token label, got nil")
	}
}

// TestPKCS11_Close_Idempotent verifies redundant Close calls are
// safe — important for defer chains and t.Cleanup nesting.
func TestPKCS11_Close_Idempotent(t *testing.T) {
	svc := newPKCS11ForTest(t)
	if err := svc.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestPKCS11_AfterClose_OperationsFail asserts that operations on a
// closed service return a typed error rather than panicking on a nil
// PKCS#11 context.
func TestPKCS11_AfterClose_OperationsFail(t *testing.T) {
	svc := newPKCS11ForTest(t)
	if err := svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ctx := context.Background()
	if _, _, err := svc.GenerateAndEncrypt(ctx, []byte("x")); err == nil {
		t.Fatal("expected error on GenerateAndEncrypt after Close, got nil")
	}
	if _, err := svc.Decrypt(ctx, dummyCID(), []byte("x")); err == nil {
		t.Fatal("expected error on Decrypt after Close, got nil")
	}
	if _, err := svc.WrapForRecipient(ctx, dummyCID(), []byte{0x04, 0x01}); err == nil {
		t.Fatal("expected error on WrapForRecipient after Close, got nil")
	}
	if err := svc.Delete(ctx, dummyCID()); err == nil {
		t.Fatal("expected error on Delete after Close, got nil")
	}
}

// TestPKCS11_WrapForRecipient_RejectsEmptyPubKey is the input-
// validation pin: ECIES wrap is impossible without a recipient pubkey,
// so the service short-circuits with the typed sentinel.
func TestPKCS11_WrapForRecipient_RejectsEmptyPubKey(t *testing.T) {
	svc := newPKCS11ForTest(t)
	cid, _, err := svc.GenerateAndEncrypt(context.Background(), []byte("hello"))
	if err != nil {
		t.Fatalf("GenerateAndEncrypt: %v", err)
	}
	_, err = svc.WrapForRecipient(context.Background(), cid, nil)
	if !errors.Is(err, artifact.ErrInvalidRecipientKey) {
		t.Fatalf("want ErrInvalidRecipientKey, got %v", err)
	}
}

// TestPKCS11_WrapForRecipient_RejectsMalformedPubKey ensures an
// almost-right-but-wrong byte string is rejected with the typed
// sentinel rather than crashing inside the secp256k1 parser.
func TestPKCS11_WrapForRecipient_RejectsMalformedPubKey(t *testing.T) {
	svc := newPKCS11ForTest(t)
	cid, _, err := svc.GenerateAndEncrypt(context.Background(), []byte("hi"))
	if err != nil {
		t.Fatalf("GenerateAndEncrypt: %v", err)
	}
	_, err = svc.WrapForRecipient(context.Background(), cid, []byte{0x04, 0x01, 0x02})
	if !errors.Is(err, artifact.ErrInvalidRecipientKey) {
		t.Fatalf("want ErrInvalidRecipientKey, got %v", err)
	}
}

// TestPKCS11_Decrypt_NonGCMPayload returns the typed
// ErrCiphertextMismatch when the GCM tag fails to authenticate. We
// produce the failure by mutating one byte of a real ciphertext.
func TestPKCS11_Decrypt_NonGCMPayload(t *testing.T) {
	svc := newPKCS11ForTest(t)
	cid, ct, err := svc.GenerateAndEncrypt(context.Background(), []byte("hello"))
	if err != nil {
		t.Fatalf("GenerateAndEncrypt: %v", err)
	}
	bad := append([]byte(nil), ct...)
	bad[0] ^= 0xff
	_, err = svc.Decrypt(context.Background(), cid, bad)
	if !errors.Is(err, artifact.ErrCiphertextMismatch) {
		t.Fatalf("want ErrCiphertextMismatch, got %v", err)
	}
}

// TestPKCS11_Decrypt_RoundTripUsingHSM is a focused unit test for
// the HSM-only decrypt path (the conformance suite covers the
// whole-system view; this one isolates that the DEK never crosses
// process memory on the round-trip happy path).
func TestPKCS11_Decrypt_RoundTripUsingHSM(t *testing.T) {
	svc := newPKCS11ForTest(t)
	plaintext := bytes.Repeat([]byte("ortholog "), 64)
	cid, ct, err := svc.GenerateAndEncrypt(context.Background(), plaintext)
	if err != nil {
		t.Fatalf("GenerateAndEncrypt: %v", err)
	}
	got, err := svc.Decrypt(context.Background(), cid, ct)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, plaintext)
	}
}

// TestPKCS11_Delete_OnUnknownCID is a no-op (idempotent). Verifies
// the not-found path doesn't escape as an error.
func TestPKCS11_Delete_OnUnknownCID(t *testing.T) {
	svc := newPKCS11ForTest(t)
	if err := svc.Delete(context.Background(), dummyCID()); err != nil {
		t.Fatalf("Delete on unknown cid should be no-op, got %v", err)
	}
}

// TestPKCS11_WrongPin asserts that an incorrect PIN surfaces at the
// first operation as a Login error (PKCS#11 modules don't validate
// the PIN at C_Initialize; the login is per-session). This exercises
// the openSession Login-error fallthrough on a non-AlreadyLoggedIn
// path.
func TestPKCS11_WrongPin(t *testing.T) {
	mod, label, _ := softhsm2Setup(t)
	svc, err := NewPKCS11(PKCS11Config{
		ModulePath: mod,
		TokenLabel: label,
		Pin:        "wrong-pin-9999",
	})
	if err != nil {
		t.Fatalf("NewPKCS11 unexpectedly failed for wrong PIN: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if _, _, err := svc.GenerateAndEncrypt(context.Background(), []byte("x")); err == nil {
		t.Fatal("expected Login error for wrong PIN, got nil")
	}
}

// TestTrimNul exercises the PKCS#11 token-label padding strip. The
// spec right-pads to 32 bytes with 0x20; some modules use 0x00.
func TestTrimNul(t *testing.T) {
	cases := []struct{ in, want string }{
		{"foo", "foo"},
		{"foo  ", "foo"},
		{"foo\x00\x00", "foo"},
		{"foo \x00 ", "foo"},
		{"", ""},
		{"\x00\x00", ""},
	}
	for _, c := range cases {
		if got := trimNul(c.in); got != c.want {
			t.Errorf("trimNul(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// dummyCID returns a CID computed over a unique sentinel byte string
// — guaranteed not to collide with any real artifact, and stable
// across runs so tests are reproducible.
func dummyCID() storage.CID {
	return storage.Compute([]byte("keyservice/pkcs11 test sentinel — does not exist"))
}
