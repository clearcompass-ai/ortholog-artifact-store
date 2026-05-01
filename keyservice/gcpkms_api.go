package keyservice

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/clearcompass-ai/ortholog-sdk/lifecycle/artifact"
)

// errGCPNotFound is the typed marker we use to translate a Firestore
// 404 (NOT_FOUND in the JSON envelope, or HTTP 404 on the REST path)
// into artifact.ErrKeyNotFound at the operation layer.
var errGCPNotFound = errors.New("keyservice/gcpkms: backend reports not-found (404)")

// kmsEncrypt wraps plaintext under the configured KEK via Cloud KMS.
// Returns the KMS-internal ciphertext bundle as opaque bytes — the
// format is GCM with KMS-internal metadata padding; we never parse
// it, just round-trip through the matching kmsDecrypt call.
func (g *GCPKMS) kmsEncrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	url := fmt.Sprintf("%s/v1/%s:encrypt", g.cfg.KMSEndpoint, g.cfg.KEKResourceName)
	body := map[string]string{"plaintext": base64.StdEncoding.EncodeToString(plaintext)}
	resp, err := g.do(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmtGCPErr(resp, "kms encrypt")
	}
	var parsed struct {
		Ciphertext string `json:"ciphertext"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("keyservice/gcpkms: decode encrypt response: %w", err)
	}
	if parsed.Ciphertext == "" {
		return nil, errors.New("keyservice/gcpkms: encrypt returned empty ciphertext")
	}
	ct, err := base64.StdEncoding.DecodeString(parsed.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("keyservice/gcpkms: decode encrypt ciphertext base64: %w", err)
	}
	return ct, nil
}

// kmsDecrypt reverses kmsEncrypt — pass the same ciphertext bytes
// and KMS returns the original plaintext under the same KEK.
func (g *GCPKMS) kmsDecrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	url := fmt.Sprintf("%s/v1/%s:decrypt", g.cfg.KMSEndpoint, g.cfg.KEKResourceName)
	body := map[string]string{"ciphertext": base64.StdEncoding.EncodeToString(ciphertext)}
	resp, err := g.do(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmtGCPErr(resp, "kms decrypt")
	}
	var parsed struct {
		Plaintext string `json:"plaintext"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("keyservice/gcpkms: decode decrypt response: %w", err)
	}
	pt, err := base64.StdEncoding.DecodeString(parsed.Plaintext)
	if err != nil {
		return nil, fmt.Errorf("keyservice/gcpkms: decode decrypt plaintext base64: %w", err)
	}
	return pt, nil
}

// firestoreCreateWrapped writes the wrapped DEK to Firestore at
// <collection>/<docID>. Uses the CreateDocument call so a duplicate
// key-write surfaces as ALREADY_EXISTS (HTTP 409) rather than
// silently overwriting.
func (g *GCPKMS) firestoreCreateWrapped(ctx context.Context, docID string, wrapped []byte) error {
	url := fmt.Sprintf("%s/v1/%s?documentId=%s",
		g.cfg.FirestoreEndpoint, g.firestoreParentPath(), docID)
	body := map[string]any{
		"fields": map[string]any{
			"wrapped": map[string]string{"bytesValue": base64.StdEncoding.EncodeToString(wrapped)},
		},
	}
	resp, err := g.do(ctx, http.MethodPost, url, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		return nil
	case http.StatusConflict:
		// Same CID already has a wrapped DEK. Indicates a duplicate
		// generation attempt — treat as a structural error rather
		// than masking it. SHA-256 collision on real content is
		// 2^-128; this is more likely a caller bug or replay.
		return fmtGCPErr(resp, "firestore createDocument (duplicate)")
	default:
		return fmtGCPErr(resp, "firestore createDocument")
	}
}

// firestoreGetWrapped retrieves the wrapped DEK at the configured
// path. Returns artifact.ErrKeyNotFound when the document is absent
// (Firestore returns HTTP 404 on missing GetDocument).
func (g *GCPKMS) firestoreGetWrapped(ctx context.Context, docID string) ([]byte, error) {
	url := fmt.Sprintf("%s/v1/%s/%s",
		g.cfg.FirestoreEndpoint, g.firestoreParentPath(), docID)
	resp, err := g.do(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, artifact.ErrKeyNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmtGCPErr(resp, "firestore getDocument")
	}
	var parsed struct {
		Fields struct {
			Wrapped struct {
				BytesValue string `json:"bytesValue"`
			} `json:"wrapped"`
		} `json:"fields"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("keyservice/gcpkms: decode firestore get: %w", err)
	}
	if parsed.Fields.Wrapped.BytesValue == "" {
		return nil, artifact.ErrKeyNotFound
	}
	wrapped, err := base64.StdEncoding.DecodeString(parsed.Fields.Wrapped.BytesValue)
	if err != nil {
		return nil, fmt.Errorf("keyservice/gcpkms: decode wrapped bytes: %w", err)
	}
	return wrapped, nil
}

// firestoreDeleteWrapped removes the document. Returns
// errGCPNotFound on 404 so the operation-layer Delete can resolve
// it to a successful idempotent no-op.
func (g *GCPKMS) firestoreDeleteWrapped(ctx context.Context, docID string) error {
	url := fmt.Sprintf("%s/v1/%s/%s",
		g.cfg.FirestoreEndpoint, g.firestoreParentPath(), docID)
	resp, err := g.do(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		return nil
	case http.StatusNotFound:
		return errGCPNotFound
	default:
		return fmtGCPErr(resp, "firestore deleteDocument")
	}
}

// do issues an HTTP request to GCP. Centralizes JSON content-type +
// transport-error wrapping. Auth headers are added by the
// oauth2-wrapped http.Client when running against real GCP; the test
// fakes ignore auth.
func (g *GCPKMS) do(ctx context.Context, method, url string, body any) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("keyservice/gcpkms: marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("keyservice/gcpkms: new request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", artifact.ErrServiceUnavailable, err)
	}
	return resp, nil
}

// fmtGCPErr reads the response body (capped) and produces a
// diagnostic error wrapping the typed sentinel for 5xx HTTP class.
// 4xx errors are caller-fault (auth, IAM, validation) and stay
// outside the ServiceUnavailable retry pool.
func fmtGCPErr(resp *http.Response, op string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	bodyStr := string(bytes.TrimSpace(body))
	if resp.StatusCode >= 500 {
		return fmt.Errorf("%w: %s HTTP %d: %s",
			artifact.ErrServiceUnavailable, op, resp.StatusCode, bodyStr)
	}
	return fmt.Errorf("keyservice/gcpkms: %s HTTP %d: %s", op, resp.StatusCode, bodyStr)
}
