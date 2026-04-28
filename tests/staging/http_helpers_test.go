//go:build staging

package staging

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// fetchURLBytes performs a plain GET with no auth and returns the body.
// Used by presigned-URL tests — the URL itself carries authorization.
//
// Pre-v7.75 this file also exported fetchURLBytesEventual for the
// Filebase-IPFS staging suite (eventual-consistency retry around
// gateway propagation). IPFS is no longer a supported backend kind;
// the helper went with the suite. Object-store signed URLs in the
// remaining Wave 3 path are immediately consistent — fetchURLBytes
// alone covers them.
func fetchURLBytes(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("GET %s: HTTP %d: %s", url, resp.StatusCode, body)
	}
	return io.ReadAll(resp.Body)
}
