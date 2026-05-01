package keyservice

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/clearcompass-ai/ortholog-sdk/lifecycle/artifact"
)

// fakeKMS is an in-process httptest server implementation of the
// GCP Cloud KMS REST API surface this backend uses (Encrypt,
// Decrypt). It is NOT a mock of our code — it's a real AES-GCM
// encryptor that the production REST helpers connect to via
// httptest.Server.URL. Same architectural pattern as our Vault
// tests, which spawn a real `vault server -dev` subprocess.
//
// fakeKMS uses a single in-process AES-256 key for the KEK and
// prepends the GCM nonce to the ciphertext so kmsDecrypt can
// recover it (matches how real Cloud KMS bundles internal metadata
// + nonce into its opaque ciphertext blob).
type fakeKMS struct {
	mu  sync.Mutex
	key []byte // 32 bytes, AES-256
}

func newFakeKMS(t *testing.T) *fakeKMS {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("fakeKMS: rand: %v", err)
	}
	return &fakeKMS{key: k}
}

func (f *fakeKMS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, ":encrypt"):
		var req struct {
			Plaintext string `json:"plaintext"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		pt, err := base64.StdEncoding.DecodeString(req.Plaintext)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ct, err := f.encrypt(pt)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"ciphertext": base64.StdEncoding.EncodeToString(ct),
		})

	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, ":decrypt"):
		var req struct {
			Ciphertext string `json:"ciphertext"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ct, err := base64.StdEncoding.DecodeString(req.Ciphertext)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		pt, err := f.decrypt(ct)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"plaintext": base64.StdEncoding.EncodeToString(pt),
		})

	default:
		http.NotFound(w, r)
	}
}

func (f *fakeKMS) encrypt(pt []byte) ([]byte, error) {
	block, err := aes.NewCipher(f.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return append(nonce, gcm.Seal(nil, nonce, pt, nil)...), nil
}

func (f *fakeKMS) decrypt(ct []byte) ([]byte, error) {
	block, err := aes.NewCipher(f.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ct) < gcm.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	return gcm.Open(nil, ct[:gcm.NonceSize()], ct[gcm.NonceSize():], nil)
}

// fakeFirestore is an in-process httptest implementation of the
// Firestore REST surface this backend uses (CreateDocument,
// GetDocument, DeleteDocument). Documents are keyed by their full
// resource name and stored as a single bytesValue field "wrapped".
type fakeFirestore struct {
	mu   sync.Mutex
	docs map[string][]byte // key: full path .../documents/<collection>/<docID>; val: wrapped bytes
}

func newFakeFirestore() *fakeFirestore {
	return &fakeFirestore{docs: make(map[string][]byte)}
}

func (f *fakeFirestore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Path shape:
	//   POST /v1/projects/X/databases/Y/documents/<collection>?documentId=Z
	//   GET/DELETE /v1/projects/X/databases/Y/documents/<collection>/<docID>
	path := strings.TrimPrefix(r.URL.Path, "/v1/")

	switch r.Method {
	case http.MethodPost:
		docID := r.URL.Query().Get("documentId")
		if docID == "" {
			http.Error(w, "missing documentId", http.StatusBadRequest)
			return
		}
		fullName := path + "/" + docID
		if _, exists := f.docs[fullName]; exists {
			http.Error(w, "ALREADY_EXISTS", http.StatusConflict)
			return
		}
		var req struct {
			Fields struct {
				Wrapped struct {
					BytesValue string `json:"bytesValue"`
				} `json:"wrapped"`
			} `json:"fields"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		wrapped, err := base64.StdEncoding.DecodeString(req.Fields.Wrapped.BytesValue)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.docs[fullName] = wrapped
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{}`)

	case http.MethodGet:
		wrapped, ok := f.docs[path]
		if !ok {
			http.Error(w, "NOT_FOUND", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"fields": map[string]any{
				"wrapped": map[string]string{"bytesValue": base64.StdEncoding.EncodeToString(wrapped)},
			},
		})

	case http.MethodDelete:
		if _, ok := f.docs[path]; !ok {
			http.Error(w, "NOT_FOUND", http.StatusNotFound)
			return
		}
		delete(f.docs, path)
		w.WriteHeader(http.StatusOK)

	default:
		http.NotFound(w, r)
	}
}

// gcpkmsFakeBackend stands up the two fakes + a configured GCPKMS
// service pointed at them. Cleanup torn down via t.Cleanup.
func gcpkmsFakeBackend(t *testing.T) *GCPKMS {
	t.Helper()
	kmsSrv := httptest.NewServer(newFakeKMS(t))
	t.Cleanup(kmsSrv.Close)
	fsSrv := httptest.NewServer(newFakeFirestore())
	t.Cleanup(fsSrv.Close)

	svc, err := NewGCPKMS(context.Background(), GCPKMSConfig{
		KEKResourceName:     "projects/test-proj/locations/us/keyRings/r/cryptoKeys/k",
		FirestoreProjectID:  "test-proj",
		FirestoreCollection: "ortholog-test",
		KMSEndpoint:         kmsSrv.URL,
		FirestoreEndpoint:   fsSrv.URL,
		HTTPClient:          http.DefaultClient,
	})
	if err != nil {
		t.Fatalf("NewGCPKMS: %v", err)
	}
	return svc
}

// TestGCPKMS_Conformance runs the SDK's shared conformance suite
// against the in-process KMS + Firestore fakes. Same RunConformance
// the InMemory, Vault Transit, and PKCS#11 backends pass.
func TestGCPKMS_Conformance(t *testing.T) {
	svc := gcpkmsFakeBackend(t)
	artifact.RunConformance(t, svc)
}

// TestGCPKMS_TrustClass pins the trust-class declaration. Cloud KMS
// HSM keeps the KEK in HSM; the DEK appears briefly in process for
// AES-GCM ops + ECIES wrap. ClassEnvelope.
func TestGCPKMS_TrustClass(t *testing.T) {
	svc := gcpkmsFakeBackend(t)
	if svc.TrustClass() != artifact.ClassEnvelope {
		t.Errorf("TrustClass = %v, want ClassEnvelope", svc.TrustClass())
	}
}

// TestNewGCPKMS_RejectsMissingFields locks the constructor's
// validation contract.
func TestNewGCPKMS_RejectsMissingFields(t *testing.T) {
	cases := []struct {
		name string
		cfg  GCPKMSConfig
	}{
		{"missing KEKResourceName", GCPKMSConfig{
			FirestoreProjectID: "p", HTTPClient: http.DefaultClient,
		}},
		{"malformed KEKResourceName", GCPKMSConfig{
			KEKResourceName: "not-a-resource-name",
			FirestoreProjectID: "p", HTTPClient: http.DefaultClient,
		}},
		{"missing FirestoreProjectID", GCPKMSConfig{
			KEKResourceName: "projects/p/locations/l/keyRings/r/cryptoKeys/k",
			HTTPClient: http.DefaultClient,
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewGCPKMS(context.Background(), c.cfg); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

// TestGCPKMS_WrapForRecipient_RejectsBadInputs covers the two
// short-circuit paths in WrapForRecipient that don't touch any
// backend at all.
func TestGCPKMS_WrapForRecipient_RejectsBadInputs(t *testing.T) {
	svc := gcpkmsFakeBackend(t)
	cid, _, err := svc.GenerateAndEncrypt(context.Background(), []byte("hi"))
	if err != nil {
		t.Fatalf("GenerateAndEncrypt: %v", err)
	}
	if _, err := svc.WrapForRecipient(context.Background(), cid, nil); !errors.Is(err, artifact.ErrInvalidRecipientKey) {
		t.Fatalf("nil pubkey: want ErrInvalidRecipientKey, got %v", err)
	}
	if _, err := svc.WrapForRecipient(context.Background(), cid, []byte{0x01, 0x02, 0x03}); !errors.Is(err, artifact.ErrInvalidRecipientKey) {
		t.Fatalf("malformed pubkey: want ErrInvalidRecipientKey, got %v", err)
	}
}

// TestGCPKMS_Decrypt_NonGCMPayload pins the GCM tag-mismatch error
// mapping to ErrCiphertextMismatch.
func TestGCPKMS_Decrypt_NonGCMPayload(t *testing.T) {
	svc := gcpkmsFakeBackend(t)
	cid, ct, err := svc.GenerateAndEncrypt(context.Background(), []byte("hello"))
	if err != nil {
		t.Fatalf("GenerateAndEncrypt: %v", err)
	}
	bad := append([]byte(nil), ct...)
	bad[0] ^= 0xff
	if _, err := svc.Decrypt(context.Background(), cid, bad); !errors.Is(err, artifact.ErrCiphertextMismatch) {
		t.Fatalf("want ErrCiphertextMismatch, got %v", err)
	}
}

// TestGCPKMS_Delete_OnUnknownCID pins the idempotent no-op contract.
func TestGCPKMS_Delete_OnUnknownCID(t *testing.T) {
	svc := gcpkmsFakeBackend(t)
	if err := svc.Delete(context.Background(), dummyCID()); err != nil {
		t.Fatalf("Delete on unknown cid should be no-op, got %v", err)
	}
}

// TestGCPKMS_ServiceUnavailable_OnUnreachable asserts that
// transport-layer errors against KMS surface as
// artifact.ErrServiceUnavailable.
func TestGCPKMS_ServiceUnavailable_OnUnreachable(t *testing.T) {
	svc, err := NewGCPKMS(context.Background(), GCPKMSConfig{
		KEKResourceName:    "projects/p/locations/l/keyRings/r/cryptoKeys/k",
		FirestoreProjectID: "p",
		KMSEndpoint:        "http://127.0.0.1:1", // closed port
		FirestoreEndpoint:  "http://127.0.0.1:1",
		HTTPClient:         &http.Client{},
	})
	if err != nil {
		t.Fatalf("NewGCPKMS: %v", err)
	}
	if _, _, err := svc.GenerateAndEncrypt(context.Background(), []byte("x")); !errors.Is(err, artifact.ErrServiceUnavailable) {
		t.Fatalf("want ErrServiceUnavailable, got %v", err)
	}
}

// TestGCPKMS_ServiceUnavailable_On5xx covers the 5xx → ServiceUnavailable
// mapping in fmtGCPErr via a fake server that always returns 500.
func TestGCPKMS_ServiceUnavailable_On5xx(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	t.Cleanup(failing.Close)
	svc, err := NewGCPKMS(context.Background(), GCPKMSConfig{
		KEKResourceName:    "projects/p/locations/l/keyRings/r/cryptoKeys/k",
		FirestoreProjectID: "p",
		KMSEndpoint:        failing.URL,
		FirestoreEndpoint:  failing.URL,
		HTTPClient:         http.DefaultClient,
	})
	if err != nil {
		t.Fatalf("NewGCPKMS: %v", err)
	}
	if _, _, err := svc.GenerateAndEncrypt(context.Background(), []byte("x")); !errors.Is(err, artifact.ErrServiceUnavailable) {
		t.Fatalf("want ErrServiceUnavailable, got %v", err)
	}
}

// TestGCPKMS_FirestoreDocPath pins the Firestore document path
// shape — the IAM policy comment in gcpkms_setup.go's package doc
// will reference these path segments.
func TestGCPKMS_FirestoreDocPath(t *testing.T) {
	svc := gcpkmsFakeBackend(t)
	cid, _, err := svc.GenerateAndEncrypt(context.Background(), []byte("hi"))
	if err != nil {
		t.Fatalf("GenerateAndEncrypt: %v", err)
	}
	got := svc.firestoreDocPath(cid)
	wantPrefix := fmt.Sprintf("projects/test-proj/databases/(default)/documents/ortholog-test/")
	if !strings.HasPrefix(got, wantPrefix) {
		t.Errorf("firestoreDocPath = %q, want prefix %q", got, wantPrefix)
	}
}
