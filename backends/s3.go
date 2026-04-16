/*
backends/s3.go — S3-compatible object storage backend.

Uses the S3 REST API via net/http. No aws-sdk-go dependency.
Works with AWS S3, MinIO, Ceph, R2, Wasabi, and any S3-compatible endpoint.
*/
package backends

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// S3RequestSigner signs outgoing S3 HTTP requests.
type S3RequestSigner interface {
	SignRequest(req *http.Request) error
}

// S3URLSigner generates presigned GET URLs.
type S3URLSigner interface {
	PresignGetObject(bucket, key string, expiry time.Duration) (string, error)
}

// S3Config holds S3 backend settings.
type S3Config struct {
	Endpoint      string // Empty = AWS default (computed from Region). Set for MinIO/R2/Ceph/testing.
	Bucket        string
	Region        string
	Prefix        string
	PathStyle     bool
	RequestSigner S3RequestSigner
	URLSigner     S3URLSigner
}

// S3Backend implements BackendProvider for S3-compatible storage.
type S3Backend struct {
	cfg    S3Config
	client *http.Client
}

func NewS3Backend(cfg S3Config) *S3Backend {
	if cfg.Endpoint == "" {
		cfg.Endpoint = fmt.Sprintf("https://s3.%s.amazonaws.com", cfg.Region)
	}
	return &S3Backend{cfg: cfg, client: &http.Client{Timeout: 60 * time.Second}}
}

func (s *S3Backend) objectURL(cid storage.CID) string {
	key := s.cfg.Prefix + cid.String()
	if s.cfg.PathStyle {
		return fmt.Sprintf("%s/%s/%s", s.cfg.Endpoint, s.cfg.Bucket, key)
	}
	endpoint := s.cfg.Endpoint
	if strings.HasPrefix(endpoint, "https://s3.") {
		endpoint = strings.Replace(endpoint, "https://s3.", fmt.Sprintf("https://%s.s3.", s.cfg.Bucket), 1)
	} else {
		endpoint = fmt.Sprintf("%s/%s", endpoint, s.cfg.Bucket)
	}
	return fmt.Sprintf("%s/%s", endpoint, key)
}

func (s *S3Backend) signRequest(req *http.Request) error {
	if s.cfg.RequestSigner != nil {
		return s.cfg.RequestSigner.SignRequest(req)
	}
	return nil
}

func (s *S3Backend) Push(cid storage.CID, data []byte) error {
	url := s.objectURL(cid)
	req, err := http.NewRequestWithContext(context.Background(), "PUT", url, strings.NewReader(string(data)))
	if err != nil {
		return fmt.Errorf("s3/push: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = int64(len(data))
	if err := s.signRequest(req); err != nil {
		return fmt.Errorf("s3/push: sign: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("s3/push: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("s3/push: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (s *S3Backend) Fetch(cid storage.CID) ([]byte, error) {
	url := s.objectURL(cid)
	req, err := http.NewRequestWithContext(context.Background(), "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("s3/fetch: %w", err)
	}
	if err := s.signRequest(req); err != nil {
		return nil, fmt.Errorf("s3/fetch: sign: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("s3/fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden {
		return nil, storage.ErrContentNotFound
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("s3/fetch: HTTP %d: %s", resp.StatusCode, body)
	}
	return io.ReadAll(resp.Body)
}

func (s *S3Backend) Exists(cid storage.CID) (bool, error) {
	url := s.objectURL(cid)
	req, err := http.NewRequestWithContext(context.Background(), "HEAD", url, nil)
	if err != nil {
		return false, fmt.Errorf("s3/exists: %w", err)
	}
	if err := s.signRequest(req); err != nil {
		return false, fmt.Errorf("s3/exists: sign: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("s3/exists: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK, nil
}

func (s *S3Backend) Pin(cid storage.CID) error {
	url := s.objectURL(cid) + "?tagging"
	body := `<?xml version="1.0" encoding="UTF-8"?><Tagging><TagSet><Tag><Key>ortholog-pinned</Key><Value>true</Value></Tag></TagSet></Tagging>`
	req, err := http.NewRequestWithContext(context.Background(), "PUT", url, strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("s3/pin: %w", err)
	}
	req.Header.Set("Content-Type", "application/xml")
	if err := s.signRequest(req); err != nil {
		return fmt.Errorf("s3/pin: sign: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("s3/pin: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return storage.ErrContentNotFound
	}
	return nil
}

func (s *S3Backend) Delete(cid storage.CID) error {
	url := s.objectURL(cid)
	req, err := http.NewRequestWithContext(context.Background(), "DELETE", url, nil)
	if err != nil {
		return fmt.Errorf("s3/delete: %w", err)
	}
	if err := s.signRequest(req); err != nil {
		return fmt.Errorf("s3/delete: sign: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("s3/delete: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return nil
}

func (s *S3Backend) Resolve(cid storage.CID, expiry time.Duration) (*storage.RetrievalCredential, error) {
	if s.cfg.URLSigner == nil {
		return nil, fmt.Errorf("s3/resolve: no URL signer configured")
	}
	key := s.cfg.Prefix + cid.String()
	url, err := s.cfg.URLSigner.PresignGetObject(s.cfg.Bucket, key, expiry)
	if err != nil {
		return nil, fmt.Errorf("s3/resolve: %w", err)
	}
	exp := time.Now().Add(expiry)
	return &storage.RetrievalCredential{Method: storage.MethodSignedURL, URL: url, Expiry: &exp}, nil
}

func (s *S3Backend) Healthy() error {
	url := s.objectURL(storage.CID{})
	if s.cfg.PathStyle {
		url = fmt.Sprintf("%s/%s", s.cfg.Endpoint, s.cfg.Bucket)
	}
	req, _ := http.NewRequestWithContext(context.Background(), "HEAD", url, nil)
	if err := s.signRequest(req); err != nil {
		return fmt.Errorf("s3/health: sign: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("s3/health: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 500 {
		return fmt.Errorf("s3/health: HTTP %d", resp.StatusCode)
	}
	return nil
}
