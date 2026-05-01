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
	"strings"

	"github.com/clearcompass-ai/ortholog-sdk/lifecycle/artifact"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// errVaultNotFound is the typed marker we use to translate Vault HTTP
// 404 responses into artifact.ErrKeyNotFound at the operation level.
// Internal — never returned to callers directly.
var errVaultNotFound = errors.New("keyservice/vault: backend reports not-found (404)")

// transitCreateKey idempotently creates a per-artifact Transit key.
// Vault returns 204 on create-or-update, 200 on existing — both fine.
func (v *VaultTransit) transitCreateKey(ctx context.Context, name string) error {
	body := map[string]any{
		"type":             "aes256-gcm96",
		"derived":          false,
		"exportable":       false,
		"allow_plaintext_backup": false,
	}
	url := fmt.Sprintf("%s/v1/%s/keys/%s", v.cfg.Endpoint, v.cfg.TransitMount, name)
	resp, err := v.do(ctx, http.MethodPost, url, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		return nil
	default:
		return fmtVaultErr(resp, "transit/keys create")
	}
}

// transitEncrypt encrypts plaintext under the named Transit key and
// returns the ciphertext blob (Vault's "vault:v1:..." format).
func (v *VaultTransit) transitEncrypt(ctx context.Context, name string, plaintext []byte) (string, error) {
	body := map[string]any{
		"plaintext": base64.StdEncoding.EncodeToString(plaintext),
	}
	url := fmt.Sprintf("%s/v1/%s/encrypt/%s", v.cfg.Endpoint, v.cfg.TransitMount, name)
	resp, err := v.do(ctx, http.MethodPost, url, body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmtVaultErr(resp, "transit/encrypt")
	}
	var parsed struct {
		Data struct {
			Ciphertext string `json:"ciphertext"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("keyservice/vault: decode encrypt response: %w", err)
	}
	if parsed.Data.Ciphertext == "" {
		return "", errors.New("keyservice/vault: encrypt returned empty ciphertext")
	}
	return parsed.Data.Ciphertext, nil
}

// transitDecrypt decrypts the Vault Transit ciphertext blob and
// returns the original plaintext bytes.
func (v *VaultTransit) transitDecrypt(ctx context.Context, name, ciphertext string) ([]byte, error) {
	body := map[string]any{"ciphertext": ciphertext}
	url := fmt.Sprintf("%s/v1/%s/decrypt/%s", v.cfg.Endpoint, v.cfg.TransitMount, name)
	resp, err := v.do(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, artifact.ErrKeyNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmtVaultErr(resp, "transit/decrypt")
	}
	var parsed struct {
		Data struct {
			Plaintext string `json:"plaintext"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("keyservice/vault: decode decrypt response: %w", err)
	}
	pt, err := base64.StdEncoding.DecodeString(parsed.Data.Plaintext)
	if err != nil {
		return nil, fmt.Errorf("keyservice/vault: decode plaintext: %w", err)
	}
	return pt, nil
}

// transitDeleteKey deletes a per-artifact Transit key. Requires
// `deletion_allowed=true` to be set on the key first; we set it
// inline. Returns errVaultNotFound for 404 (idempotent caller path).
//
// Idempotency note: Vault returns HTTP 400 (not 404) when configuring
// or deleting a key that does not exist. We probe with a HEAD-style
// existence check first via GET /transit/keys/<name> so the
// double-delete path resolves cleanly to errVaultNotFound rather than
// a confusing 400.
func (v *VaultTransit) transitDeleteKey(ctx context.Context, name string) error {
	// Existence check — Vault returns 404 on a missing key here.
	getURL := fmt.Sprintf("%s/v1/%s/keys/%s", v.cfg.Endpoint, v.cfg.TransitMount, name)
	getResp, err := v.do(ctx, http.MethodGet, getURL, nil)
	if err != nil {
		return err
	}
	getResp.Body.Close()
	if getResp.StatusCode == http.StatusNotFound {
		return errVaultNotFound
	}
	if getResp.StatusCode != http.StatusOK {
		return fmtVaultErr(getResp, "transit/keys exists-check")
	}

	// Allow deletion (configurable on each transit key in Vault).
	cfgURL := fmt.Sprintf("%s/v1/%s/keys/%s/config", v.cfg.Endpoint, v.cfg.TransitMount, name)
	cfgResp, err := v.do(ctx, http.MethodPost, cfgURL, map[string]any{"deletion_allowed": true})
	if err != nil {
		return err
	}
	cfgResp.Body.Close()
	if cfgResp.StatusCode != http.StatusOK && cfgResp.StatusCode != http.StatusNoContent {
		return fmtVaultErr(cfgResp, "transit/keys config")
	}

	resp, err := v.do(ctx, http.MethodDelete, getURL, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		return nil
	case http.StatusNotFound:
		return errVaultNotFound
	default:
		return fmtVaultErr(resp, "transit/keys delete")
	}
}

// kvPutWrapped stores the Transit-wrapped DEK in kv-v2 indexed by CID.
func (v *VaultTransit) kvPutWrapped(ctx context.Context, cid storage.CID, wrapped string) error {
	body := map[string]any{
		"data": map[string]string{
			"wrapped": wrapped,
		},
	}
	url := fmt.Sprintf("%s%s", v.cfg.Endpoint, v.kvDataPath(cid))
	resp, err := v.do(ctx, http.MethodPost, url, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent:
		return nil
	default:
		return fmtVaultErr(resp, "kv-v2 put")
	}
}

// kvGetWrapped retrieves the Transit-wrapped DEK for cid. Returns
// artifact.ErrKeyNotFound if the kv-v2 secret is missing or has been
// deleted (which produces a 404 on the data endpoint).
func (v *VaultTransit) kvGetWrapped(ctx context.Context, cid storage.CID) (string, error) {
	url := fmt.Sprintf("%s%s", v.cfg.Endpoint, v.kvDataPath(cid))
	resp, err := v.do(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", artifact.ErrKeyNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmtVaultErr(resp, "kv-v2 get")
	}
	var parsed struct {
		Data struct {
			Data struct {
				Wrapped string `json:"wrapped"`
			} `json:"data"`
			Metadata struct {
				DeletionTime string `json:"deletion_time"`
			} `json:"metadata"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("keyservice/vault: decode kv-v2 get: %w", err)
	}
	// kv-v2 soft-delete returns 200 with deletion_time set and empty
	// data. Treat as not-found.
	if parsed.Data.Metadata.DeletionTime != "" || parsed.Data.Data.Wrapped == "" {
		return "", artifact.ErrKeyNotFound
	}
	return parsed.Data.Data.Wrapped, nil
}

// kvDeleteMetadata permanently removes all versions of a kv-v2 secret.
// Returns errVaultNotFound on 404 (idempotent).
func (v *VaultTransit) kvDeleteMetadata(ctx context.Context, cid storage.CID) error {
	url := fmt.Sprintf("%s%s", v.cfg.Endpoint, v.kvMetadataPath(cid))
	resp, err := v.do(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		return nil
	case http.StatusNotFound:
		return errVaultNotFound
	default:
		return fmtVaultErr(resp, "kv-v2 metadata delete")
	}
}

// do issues an HTTP request to Vault. Centralizes auth header
// + JSON content-type + transport-error wrapping.
func (v *VaultTransit) do(ctx context.Context, method, url string, body any) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("keyservice/vault: marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("keyservice/vault: new request: %w", err)
	}
	req.Header.Set("X-Vault-Token", v.cfg.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", artifact.ErrServiceUnavailable, err)
	}
	return resp, nil
}

// fmtVaultErr reads the response body (capped) and produces a
// diagnostic error wrapping the typed sentinel for the HTTP class.
func fmtVaultErr(resp *http.Response, op string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	bodyStr := strings.TrimSpace(string(body))
	if resp.StatusCode >= 500 {
		return fmt.Errorf("%w: %s HTTP %d: %s",
			artifact.ErrServiceUnavailable, op, resp.StatusCode, bodyStr)
	}
	return fmt.Errorf("keyservice/vault: %s HTTP %d: %s", op, resp.StatusCode, bodyStr)
}

// kvDataPath returns the kv-v2 data path for cid.
func (v *VaultTransit) kvDataPath(cid storage.CID) string {
	return fmt.Sprintf("/v1/%s/data/%s/%s",
		v.cfg.KVMount, v.cfg.KVNamespace, hexCID(cid))
}

// kvMetadataPath returns the kv-v2 metadata path for cid.
func (v *VaultTransit) kvMetadataPath(cid storage.CID) string {
	return fmt.Sprintf("/v1/%s/metadata/%s/%s",
		v.cfg.KVMount, v.cfg.KVNamespace, hexCID(cid))
}

// hexCID returns the SHA-256 digest portion of a CID as a hex string.
// Used as the path-segment identifier in both Transit and kv-v2 paths.
func hexCID(cid storage.CID) string {
	const hexChars = "0123456789abcdef"
	out := make([]byte, len(cid.Digest)*2)
	for i, b := range cid.Digest {
		out[i*2] = hexChars[b>>4]
		out[i*2+1] = hexChars[b&0x0f]
	}
	return string(out)
}
