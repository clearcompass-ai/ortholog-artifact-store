/*
backends/rustfs.go — RustFS object storage backend.

# Protocol vs. implementation

This backend speaks the S3 wire protocol (PUT/GET/HEAD/DELETE on
/<bucket>/<key>, SigV4 request signing, presigned GET URLs). RustFS is
the supported implementation of that protocol — a Rust-native, self-
hosted object store with the operational profile we want (simple binary,
SigV4 auth, predictable behavior on small-object-heavy workloads).

The protocol-level code in this file (URL construction, request signing,
status-code classification) is generic to the S3 wire protocol. Calling
the type RustFSBackend is a vocabulary choice: the artifact store ships
with three named backends — GCS, RustFS, IPFS — and the deployment story
talks about RustFS, not "S3-compatible vendors." There is no other
S3-protocol implementation in the supported set.

# What this file does

  - PUT  /<bucket>/<key>           Push
  - GET  /<bucket>/<key>           Fetch
  - HEAD /<bucket>/<key>           Exists
  - PUT  /<bucket>/<key>?tagging   Pin (sets ortholog-pinned tag)
  - DEL  /<bucket>/<key>           Delete
  - presigned GET URL              Resolve (MethodSignedURL)

Uses net/http directly — no aws-sdk-go dependency. SigV4 signing is
provided by an injected RequestSigner; presigned URL generation by an
injected URLSigner. Both interfaces are defined here; concrete
implementations live in internal/signers/.
*/
package backends

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	sdklog "github.com/clearcompass-ai/ortholog-sdk/log"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// RustFSRequestSigner signs outgoing S3-protocol HTTP requests (SigV4).
type RustFSRequestSigner interface {
	SignRequest(req *http.Request) error
}

// RustFSURLSigner generates presigned GET URLs for retrieval credentials.
type RustFSURLSigner interface {
	PresignGetObject(bucket, key string, expiry time.Duration) (string, error)
}

// RustFSConfig holds RustFS backend settings.
type RustFSConfig struct {
	Endpoint      string // RustFS endpoint, e.g. http://rustfs.internal:9000
	Bucket        string
	Region        string
	Prefix        string
	PathStyle     bool
	RequestSigner RustFSRequestSigner
	URLSigner     RustFSURLSigner
}

// RustFSBackend implements BackendProvider for RustFS object storage.
type RustFSBackend struct {
	cfg    RustFSConfig
	client *http.Client
}

// NewRustFSBackend constructs a RustFS-backed BackendProvider.
// The endpoint is required — there is no implicit hostname inference.
//
// HTTP transport: sdklog.DefaultClient(60s) — connection pool of
// 100 idle conns/host (vs stdlib's 2) plus the SDK's
// RetryAfterRoundTripper that honors RustFS / S3-wire 503 +
// Retry-After responses transparently. S3-protocol implementations
// surface 503 + Retry-After under load; honoring it locally
// preserves Push/Fetch availability through transient pressure.
func NewRustFSBackend(cfg RustFSConfig) *RustFSBackend {
	return &RustFSBackend{cfg: cfg, client: sdklog.DefaultClient(60 * time.Second)}
}

func (s *RustFSBackend) objectURL(cid storage.CID) string {
	key := s.cfg.Prefix + cid.String()
	if s.cfg.PathStyle {
		return fmt.Sprintf("%s/%s/%s", s.cfg.Endpoint, s.cfg.Bucket, key)
	}
	endpoint := fmt.Sprintf("%s/%s", s.cfg.Endpoint, s.cfg.Bucket)
	return fmt.Sprintf("%s/%s", endpoint, key)
}

func (s *RustFSBackend) signRequest(req *http.Request) error {
	if s.cfg.RequestSigner != nil {
		return s.cfg.RequestSigner.SignRequest(req)
	}
	return nil
}

func (s *RustFSBackend) Push(cid storage.CID, data []byte) error {
	url := s.objectURL(cid)
	req, err := http.NewRequestWithContext(context.Background(), "PUT", url, strings.NewReader(string(data)))
	if err != nil {
		return fmt.Errorf("rustfs/push: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = int64(len(data))
	if err := s.signRequest(req); err != nil {
		return fmt.Errorf("rustfs/push: sign: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("rustfs/push: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("rustfs/push: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (s *RustFSBackend) Fetch(cid storage.CID) ([]byte, error) {
	url := s.objectURL(cid)
	req, err := http.NewRequestWithContext(context.Background(), "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("rustfs/fetch: %w", err)
	}
	if err := s.signRequest(req); err != nil {
		return nil, fmt.Errorf("rustfs/fetch: sign: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rustfs/fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden {
		return nil, storage.ErrContentNotFound
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("rustfs/fetch: HTTP %d: %s", resp.StatusCode, body)
	}
	return io.ReadAll(resp.Body)
}

func (s *RustFSBackend) Exists(cid storage.CID) (bool, error) {
	url := s.objectURL(cid)
	req, err := http.NewRequestWithContext(context.Background(), "HEAD", url, nil)
	if err != nil {
		return false, fmt.Errorf("rustfs/exists: %w", err)
	}
	if err := s.signRequest(req); err != nil {
		return false, fmt.Errorf("rustfs/exists: sign: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("rustfs/exists: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK, nil
}

func (s *RustFSBackend) Pin(cid storage.CID) error {
	url := s.objectURL(cid) + "?tagging"
	body := `<?xml version="1.0" encoding="UTF-8"?><Tagging><TagSet><Tag><Key>ortholog-pinned</Key><Value>true</Value></Tag></TagSet></Tagging>`
	req, err := http.NewRequestWithContext(context.Background(), "PUT", url, strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("rustfs/pin: %w", err)
	}
	req.Header.Set("Content-Type", "application/xml")
	if err := s.signRequest(req); err != nil {
		return fmt.Errorf("rustfs/pin: sign: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("rustfs/pin: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return storage.ErrContentNotFound
	}
	return nil
}

func (s *RustFSBackend) Delete(cid storage.CID) error {
	url := s.objectURL(cid)
	req, err := http.NewRequestWithContext(context.Background(), "DELETE", url, nil)
	if err != nil {
		return fmt.Errorf("rustfs/delete: %w", err)
	}
	if err := s.signRequest(req); err != nil {
		return fmt.Errorf("rustfs/delete: sign: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("rustfs/delete: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	// S3 DELETE is idempotent: AWS and RustFS both return 204 No
	// Content whether the object existed or not. So we don't map a
	// 404 to ErrContentNotFound here (the GCS backend does because
	// GCS does signal not-found on DELETE — different vendor
	// contract). Anything outside 2xx is a real failure (auth,
	// quota, server error) that previously was silently swallowed;
	// surfacing it is the whole point of this change.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("rustfs/delete: HTTP %d: %s", resp.StatusCode, body)
	}
	return nil
}

func (s *RustFSBackend) Resolve(cid storage.CID, expiry time.Duration) (*storage.RetrievalCredential, error) {
	if s.cfg.URLSigner == nil {
		return nil, fmt.Errorf("rustfs/resolve: no URL signer configured")
	}
	key := s.cfg.Prefix + cid.String()
	url, err := s.cfg.URLSigner.PresignGetObject(s.cfg.Bucket, key, expiry)
	if err != nil {
		return nil, fmt.Errorf("rustfs/resolve: %w", err)
	}
	exp := time.Now().Add(expiry)
	return &storage.RetrievalCredential{Method: storage.MethodSignedURL, URL: url, Expiry: &exp}, nil
}

func (s *RustFSBackend) Healthy() error {
	url := s.objectURL(storage.CID{})
	if s.cfg.PathStyle {
		url = fmt.Sprintf("%s/%s", s.cfg.Endpoint, s.cfg.Bucket)
	}
	req, _ := http.NewRequestWithContext(context.Background(), "HEAD", url, nil)
	if err := s.signRequest(req); err != nil {
		return fmt.Errorf("rustfs/health: sign: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("rustfs/health: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 500 {
		return fmt.Errorf("rustfs/health: HTTP %d", resp.StatusCode)
	}
	return nil
}
