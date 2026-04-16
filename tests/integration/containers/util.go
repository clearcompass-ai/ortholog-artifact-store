//go:build integration

package containers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// imageOrDefault lets CI pin to specific image tags without forcing
// developers to edit source. Example: MINIO_IMAGE=minio/minio:RELEASE.2024-01-01T00-00-00Z
// gives reproducible container builds without tagging us to :latest.
func imageOrDefault(envVar, fallback string) string {
	if v := os.Getenv(envVar); v != "" {
		return v
	}
	return fallback
}

// doJSONPost sends a JSON body with Content-Type set. Used by the
// fake-gcs-server bucket-creation helper.
func doJSONPost(ctx context.Context, url, body string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusConflict {
		return fmt.Errorf("POST %s: HTTP %d", url, resp.StatusCode)
	}
	return nil
}

// doSignedRequest sends a SigV4-signed request and waits for a 2xx.
// Used by the MinIO bucket-creation helper.
func doSignedRequest(ctx context.Context, method, url string, body io.Reader, signer *SigV4Signer) error {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	if err := signer.SignRequest(req); err != nil {
		return fmt.Errorf("sign: %w", err)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusConflict {
		return fmt.Errorf("%s %s: HTTP %d", method, url, resp.StatusCode)
	}
	return nil
}
