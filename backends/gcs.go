/*
backends/gcs.go — Google Cloud Storage backend.

Uses the GCS JSON API via net/http. No cloud.google.com/go/storage dependency.
Authentication: Application Default Credentials (ADC) via OAuth2 token.

Signed URL generation for Resolve uses a URLSigner interface. Production
deployments inject a signer backed by a service account key or IAM
signBlob API. Tests inject a mock signer.
*/
package backends

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// GCSURLSigner generates V4 signed URLs for GCS objects.
type GCSURLSigner interface {
	SignURL(bucket, object string, expiry time.Duration) (string, error)
}

// GCSConfig holds GCS backend settings.
type GCSConfig struct {
	Bucket    string
	Prefix    string
	TokenFunc func() (string, error)
	URLSigner GCSURLSigner

	// BaseURL overrides the default storage.googleapis.com endpoint.
	// Tests set this to point at an httptest.Server fake. Empty = production.
	BaseURL string
}

// GCSBackend implements BackendProvider for Google Cloud Storage.
type GCSBackend struct {
	cfg    GCSConfig
	client *http.Client
}

// NewGCSBackend creates a GCS backend.
func NewGCSBackend(cfg GCSConfig) *GCSBackend {
	return &GCSBackend{cfg: cfg, client: &http.Client{Timeout: 60 * time.Second}}
}

func (g *GCSBackend) baseURL() string {
	if g.cfg.BaseURL != "" {
		return g.cfg.BaseURL
	}
	return "https://storage.googleapis.com"
}

func (g *GCSBackend) objectPath(cid storage.CID) string { return g.cfg.Prefix + cid.String() }

// URL helpers.
//
// Per the GCS JSON API spec, object names embedded in a URL must be
// URL-encoded so that special characters (notably "/" and ":") in
// the name are not parsed as URL structure. The artifact store keys
// objects by `<prefix><cid.String()>`, where prefix can include "/"
// (e.g. "staging/<test>/<nonce>/") and cid.String() always contains
// a ":" (e.g. "sha256:<hex>"). Without encoding, GET/DELETE/PATCH
// against the path-segment form `/storage/v1/b/<bucket>/o/<name>`
// silently 404s on real GCS — the unencoded "/"s in the name are
// interpreted as further URL path elements rather than part of the
// object name.
//
// Push (?uploadType=media&name=...) uses the query-string form, where
// historically GCS tolerated unencoded "/"; we still encode here for
// correctness and to keep behavior identical between regimes.

func (g *GCSBackend) apiURL(object string) string {
	return fmt.Sprintf("%s/upload/storage/v1/b/%s/o?uploadType=media&name=%s",
		g.baseURL(), g.cfg.Bucket, url.QueryEscape(object))
}

func (g *GCSBackend) objectURL(object string) string {
	return fmt.Sprintf("%s/storage/v1/b/%s/o/%s",
		g.baseURL(), g.cfg.Bucket, url.PathEscape(object))
}

func (g *GCSBackend) mediaURL(object string) string {
	return fmt.Sprintf("%s/storage/v1/b/%s/o/%s?alt=media",
		g.baseURL(), g.cfg.Bucket, url.PathEscape(object))
}

func (g *GCSBackend) authHeader() (string, error) {
	if g.cfg.TokenFunc == nil {
		return "", nil
	}
	token, err := g.cfg.TokenFunc()
	if err != nil {
		return "", fmt.Errorf("gcs: get token: %w", err)
	}
	return "Bearer " + token, nil
}

func (g *GCSBackend) Push(cid storage.CID, data []byte) error {
	object := g.objectPath(cid)
	req, err := http.NewRequestWithContext(context.Background(), "POST", g.apiURL(object), strings.NewReader(string(data)))
	if err != nil {
		return fmt.Errorf("gcs/push: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if auth, err := g.authHeader(); err != nil {
		return err
	} else if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("gcs/push: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("gcs/push: HTTP %d: %s", resp.StatusCode, body)
	}
	return nil
}

func (g *GCSBackend) Fetch(cid storage.CID) ([]byte, error) {
	object := g.objectPath(cid)
	req, err := http.NewRequestWithContext(context.Background(), "GET", g.mediaURL(object), nil)
	if err != nil {
		return nil, fmt.Errorf("gcs/fetch: %w", err)
	}
	if auth, err := g.authHeader(); err != nil {
		return nil, err
	} else if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gcs/fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, storage.ErrContentNotFound
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("gcs/fetch: HTTP %d: %s", resp.StatusCode, body)
	}
	return io.ReadAll(resp.Body)
}

func (g *GCSBackend) Exists(cid storage.CID) (bool, error) {
	object := g.objectPath(cid)
	req, err := http.NewRequestWithContext(context.Background(), "GET", g.objectURL(object), nil)
	if err != nil {
		return false, fmt.Errorf("gcs/exists: %w", err)
	}
	if auth, err := g.authHeader(); err != nil {
		return false, err
	} else if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("gcs/exists: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK, nil
}

func (g *GCSBackend) Pin(cid storage.CID) error {
	object := g.objectPath(cid)
	body := `{"metadata":{"ortholog-pinned":"true"}}`
	req, err := http.NewRequestWithContext(context.Background(), "PATCH", g.objectURL(object), strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("gcs/pin: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if auth, err := g.authHeader(); err != nil {
		return err
	} else if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("gcs/pin: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return storage.ErrContentNotFound
	}
	return nil
}

func (g *GCSBackend) Delete(cid storage.CID) error {
	object := g.objectPath(cid)
	req, err := http.NewRequestWithContext(context.Background(), "DELETE", g.objectURL(object), nil)
	if err != nil {
		return fmt.Errorf("gcs/delete: %w", err)
	}
	if auth, err := g.authHeader(); err != nil {
		return err
	} else if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("gcs/delete: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	// GCS returns 204 No Content on a successful delete; 200 is also
	// observed for some legacy paths. 404 is "object not present" —
	// surfaced distinctly so callers can tell "delete succeeded" from
	// "delete found nothing to do." Anything else is a real failure
	// (auth, quota, server error) that previously was silently
	// swallowed; surfacing it is the whole point of this change.
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		return nil
	case http.StatusNotFound:
		return storage.ErrContentNotFound
	default:
		return fmt.Errorf("gcs/delete: HTTP %d: %s", resp.StatusCode, body)
	}
}

func (g *GCSBackend) Resolve(cid storage.CID, expiry time.Duration) (*storage.RetrievalCredential, error) {
	if g.cfg.URLSigner == nil {
		return nil, fmt.Errorf("gcs/resolve: no URL signer configured")
	}
	object := g.objectPath(cid)
	url, err := g.cfg.URLSigner.SignURL(g.cfg.Bucket, object, expiry)
	if err != nil {
		return nil, fmt.Errorf("gcs/resolve: %w", err)
	}
	exp := time.Now().Add(expiry)
	return &storage.RetrievalCredential{Method: storage.MethodSignedURL, URL: url, Expiry: &exp}, nil
}

func (g *GCSBackend) Healthy() error {
	url := fmt.Sprintf("%s/storage/v1/b/%s", g.baseURL(), g.cfg.Bucket)
	req, _ := http.NewRequestWithContext(context.Background(), "GET", url, nil)
	if auth, err := g.authHeader(); err != nil {
		return err
	} else if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("gcs/health: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gcs/health: HTTP %d", resp.StatusCode)
	}
	return nil
}

// suppress unused import
var _ = json.Marshal
