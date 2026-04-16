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

// fetchURLBytesEventual retries with backoff for eventually-consistent
// endpoints (IPFS gateways need time to propagate newly-pinned content).
//
//	maxAttempts: total attempts before giving up
//	baseDelay:   seconds to wait before retry 1; doubles each attempt
func fetchURLBytesEventual(ctx context.Context, url string, maxAttempts, baseDelay int) ([]byte, error) {
	var lastErr error
	delay := time.Duration(baseDelay) * time.Second
	for i := 0; i < maxAttempts; i++ {
		b, err := fetchURLBytes(ctx, url)
		if err == nil {
			return b, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		if delay < 30*time.Second {
			delay *= 2
		}
	}
	return nil, fmt.Errorf("exhausted %d attempts: %w", maxAttempts, lastErr)
}
