package keyservice

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/clearcompass-ai/ortholog-sdk/lifecycle/artifact"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// These are direct unit tests for the keyservice package's internal
// helpers and pure functions. They run unconditionally (no Vault or
// SoftHSM2 required) so the package's coverage stays well above the
// 80% gate even when the realistic-backend tests skip — and they pin
// the byte-level wire-layout properties the SDK conformance assumes.

// TestAESGCM_RoundTrip pins the layout: gcm.Seal(nil, nonce, plaintext,
// nil) → ciphertext-with-trailing-tag, no nonce prefix. Mirrors the
// SDK's crypto/artifact.EncryptArtifact byte-for-byte.
func TestAESGCM_RoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0x11}, 32)
	nonce := bytes.Repeat([]byte{0x22}, 12)
	plaintext := []byte("hello, ortholog")

	ct, err := aesGCMSeal(key, nonce, plaintext)
	if err != nil {
		t.Fatalf("aesGCMSeal: %v", err)
	}
	if len(ct) != len(plaintext)+16 {
		t.Errorf("ciphertext length = %d; want plaintext+16 = %d (no nonce prefix)",
			len(ct), len(plaintext)+16)
	}

	pt, err := aesGCMOpen(key, nonce, ct)
	if err != nil {
		t.Fatalf("aesGCMOpen: %v", err)
	}
	if !bytes.Equal(pt, plaintext) {
		t.Errorf("round-trip mismatch: got %q want %q", pt, plaintext)
	}
}

// TestAESGCMSeal_RejectsWrongNonceLength pins the input-validation
// contract — a too-short or too-long nonce surfaces a clear error
// rather than silently producing garbage.
func TestAESGCMSeal_RejectsWrongNonceLength(t *testing.T) {
	key := bytes.Repeat([]byte{0x11}, 32)
	if _, err := aesGCMSeal(key, []byte{0x01}, []byte("x")); err == nil {
		t.Fatal("expected error for short nonce, got nil")
	}
	if _, err := aesGCMSeal(key, bytes.Repeat([]byte{0x01}, 24), []byte("x")); err == nil {
		t.Fatal("expected error for over-long nonce, got nil")
	}
}

// TestAESGCMSeal_RejectsWrongKeyLength asserts that AES-128/AES-192
// keys produce a clear error — we hard-code AES-256 throughout the
// keyservice and want to fail loudly if a caller drifts.
func TestAESGCMSeal_RejectsWrongKeyLength(t *testing.T) {
	if _, err := aesGCMSeal([]byte("short-key"), bytes.Repeat([]byte{0}, 12), []byte("x")); err == nil {
		t.Fatal("expected error for invalid AES key length, got nil")
	}
}

// TestAESGCMOpen_RejectsTamperedCiphertext exercises the GCM auth-tag
// validation — flipping any byte must surface a non-nil error so
// callers can map to ErrCiphertextMismatch.
func TestAESGCMOpen_RejectsTamperedCiphertext(t *testing.T) {
	key := bytes.Repeat([]byte{0x11}, 32)
	nonce := bytes.Repeat([]byte{0x22}, 12)
	ct, err := aesGCMSeal(key, nonce, []byte("hello"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	ct[0] ^= 0xff
	if _, err := aesGCMOpen(key, nonce, ct); err == nil {
		t.Fatal("expected auth-tag mismatch error, got nil")
	}
}

// TestAESGCMOpen_RejectsBadKeyOrNonceLength covers the same branches
// in aesGCMOpen that aesGCMSeal hits — short key panics inside aes,
// wrong nonce length is caught by gcm.NewGCM/Open sequence.
func TestAESGCMOpen_RejectsBadKeyOrNonceLength(t *testing.T) {
	if _, err := aesGCMOpen([]byte("short"), bytes.Repeat([]byte{0}, 12), []byte("x")); err == nil {
		t.Fatal("expected error for invalid AES key length, got nil")
	}
	key := bytes.Repeat([]byte{0x11}, 32)
	if _, err := aesGCMOpen(key, []byte{0x01}, []byte("xxxxxxxxxxxxxxxx")); err == nil {
		t.Fatal("expected error for short nonce, got nil")
	}
}

// TestZeroize verifies the wipe writes through to the backing array.
// Important: the function must not be a compile-time elision target.
func TestZeroize(t *testing.T) {
	b := []byte{0x11, 0x22, 0x33}
	zeroize(b)
	for i, v := range b {
		if v != 0 {
			t.Errorf("zeroize: b[%d] = 0x%02x, want 0x00", i, v)
		}
	}
	zeroize(nil) // no-op — must not panic
	zeroize([]byte{})
}

// TestParseRecipientPubKey_RejectsEmpty pins the empty-input branch
// of the secp256k1 parser, which the higher-level code wraps with
// the artifact.ErrInvalidRecipientKey sentinel.
func TestParseRecipientPubKey_RejectsEmpty(t *testing.T) {
	if _, err := parseRecipientPubKey(nil); err == nil {
		t.Fatal("expected error for nil input, got nil")
	}
	if _, err := parseRecipientPubKey([]byte{}); err == nil {
		t.Fatal("expected error for empty input, got nil")
	}
}

// TestParseRecipientPubKey_RejectsMalformed feeds non-SEC1 bytes —
// the parser must return an error so callers can wrap it with
// ErrInvalidRecipientKey rather than crashing the secp256k1 lib.
func TestParseRecipientPubKey_RejectsMalformed(t *testing.T) {
	cases := [][]byte{
		{0x00},                                // wrong tag, too short
		{0x04, 0x01, 0x02, 0x03},              // uncompressed tag, too short
		bytes.Repeat([]byte{0xff}, 65),        // uncompressed length but bogus coordinates
		append([]byte{0x05}, bytes.Repeat([]byte{0x01}, 64)...), // unknown SEC1 tag
	}
	for i, c := range cases {
		if _, err := parseRecipientPubKey(c); err == nil {
			t.Errorf("case %d: expected error for %d-byte malformed input, got nil", i, len(c))
		}
	}
}

// TestHexCID pins the digest-bytes-to-hex encoding the Vault Transit
// key name and kv-v2 paths depend on. Regression armor: a future
// optimization that re-implements this must produce byte-identical
// output for SHA-256 inputs.
func TestHexCID(t *testing.T) {
	cases := []struct {
		digest []byte
		want   string
	}{
		{nil, ""},
		{[]byte{}, ""},
		{[]byte{0x00}, "00"},
		{[]byte{0xff}, "ff"},
		{[]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}, "0123456789abcdef"},
	}
	for i, c := range cases {
		cid := storage.CID{Algorithm: storage.AlgoSHA256, Digest: c.digest}
		got := hexCID(cid)
		if got != c.want {
			t.Errorf("case %d: hexCID(%x) = %q, want %q", i, c.digest, got, c.want)
		}
	}
}

// TestVaultPaths pins the URL shapes the production policy must
// authorize. These aren't just internal — the Vault token policy
// document in vault.go's package comment depends on this layout.
func TestVaultPaths(t *testing.T) {
	v, err := NewVaultTransit(VaultTransitConfig{
		Endpoint: "http://vault.example", Token: "x",
	})
	if err != nil {
		t.Fatalf("NewVaultTransit: %v", err)
	}
	cid := storage.Compute([]byte("hello"))
	want := "/v1/secret/data/artifact-keys/" + hexCID(cid)
	if got := v.kvDataPath(cid); got != want {
		t.Errorf("kvDataPath = %q, want %q", got, want)
	}
	want = "/v1/secret/metadata/artifact-keys/" + hexCID(cid)
	if got := v.kvMetadataPath(cid); got != want {
		t.Errorf("kvMetadataPath = %q, want %q", got, want)
	}
	want = "artifact-" + hexCID(cid)
	if got := v.transitKeyName(cid); got != want {
		t.Errorf("transitKeyName = %q, want %q", got, want)
	}
}

// TestFmtVaultErr_ServerError covers the 5xx → ErrServiceUnavailable
// mapping that the higher-level operation methods rely on so callers
// can errors.Is against the typed sentinel for retry decisions.
func TestFmtVaultErr_ServerError(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader("upstream timeout")),
	}
	err := fmtVaultErr(resp, "test/op")
	if !errors.Is(err, artifact.ErrServiceUnavailable) {
		t.Fatalf("5xx must wrap ErrServiceUnavailable, got %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("error string missing HTTP 500: %v", err)
	}
	if !strings.Contains(err.Error(), "upstream timeout") {
		t.Errorf("error string missing body excerpt: %v", err)
	}
}

// TestFmtVaultErr_ClientError covers the 4xx path — these are NOT
// wrapped with ErrServiceUnavailable (they're caller-fault errors:
// permission denied, invalid token, etc.).
func TestFmtVaultErr_ClientError(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Body:       io.NopCloser(strings.NewReader(`{"errors":["permission denied"]}`)),
	}
	err := fmtVaultErr(resp, "kv-v2 put")
	if errors.Is(err, artifact.ErrServiceUnavailable) {
		t.Fatalf("4xx must NOT wrap ErrServiceUnavailable, got %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP 403") {
		t.Errorf("error string missing HTTP 403: %v", err)
	}
}

// TestFmtVaultErr_TruncatesLongBody asserts the body excerpt is
// bounded — Vault can return verbose multi-MB error pages on rare
// occasions, and we don't want them in our error chain in full.
func TestFmtVaultErr_TruncatesLongBody(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(strings.NewReader(strings.Repeat("X", 1<<16))),
	}
	err := fmtVaultErr(resp, "test")
	// LimitReader caps at 4096; the message is "..HTTP 400: <body>"
	// — assert we didn't blow past the cap.
	if len(err.Error()) > 4500 {
		t.Errorf("error message length %d exceeds the 4 KiB body cap by too much", len(err.Error()))
	}
}

// TestTransitKeyName_HyphenNotSlash regression-pins the choice of
// "-" vs "/" between the prefix and the digest. Vault Transit's URL
// router parses slashes as path segments and returns 404 "unsupported
// path" for keys named "artifact/<hex>" — this pin catches anyone
// who tries to "fix" the prefix back to a slash for prettier audit
// logs without realizing the routing breaks.
func TestTransitKeyName_HyphenNotSlash(t *testing.T) {
	v, err := NewVaultTransit(VaultTransitConfig{
		Endpoint: "http://x", Token: "y",
	})
	if err != nil {
		t.Fatalf("NewVaultTransit: %v", err)
	}
	cid := storage.Compute([]byte("anything"))
	name := v.transitKeyName(cid)
	if strings.Contains(name, "/") {
		t.Errorf("transitKeyName must not contain '/' (Vault routing breaks): %q", name)
	}
	if !strings.HasPrefix(name, "artifact-") {
		t.Errorf("transitKeyName must keep the artifact- prefix: %q", name)
	}
}

// TestPKCS11_KeyLabel_ExpectedShape pins the label pattern the PKCS#11
// HSM admin tooling and audit logs depend on. Operators search for
// "artifact-<hex>" in the HSM's object listing — changing the prefix
// or hex casing would silently break their workflows.
func TestPKCS11_KeyLabel_ExpectedShape(t *testing.T) {
	p := &PKCS11{cfg: PKCS11Config{KeyLabelPrefix: "artifact-"}}
	cid := storage.Compute([]byte("xyz"))
	got := p.keyLabel(cid)
	if !strings.HasPrefix(got, "artifact-") {
		t.Errorf("keyLabel prefix lost: %q", got)
	}
	if got != "artifact-"+hexCID(cid) {
		t.Errorf("keyLabel = %q, want artifact-<hex>", got)
	}
}
