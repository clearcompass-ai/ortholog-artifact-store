//go:build staging

package staging

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
)

// randomPrefix returns a bucket-key prefix unique to each test run.
// The prefix starts with "staging/" and includes the hostname's first
// 8 bytes hashed-out + a random nonce so parallel CI runs across
// different commits don't collide on the same bucket.
//
// Shape: staging/{TestName}/{8-random-hex}/
//
// Buckets used for Wave 3 have a lifecycle rule deleting everything under
// the staging/ prefix after 24 hours. This keeps storage costs bounded
// even if a test run crashes and skips its own cleanup.
func randomPrefix(t *testing.T) string {
	t.Helper()
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	name := strings.ReplaceAll(t.Name(), "/", "_")
	return fmt.Sprintf("staging/%s/%s/", name, hex.EncodeToString(buf[:]))
}

// operationCount is a cheap per-run counter of cloud API operations.
// Each suite increments it via recordOp(t). At the end of a run, a
// summary test logs the count. This does NOT enforce a hard cap — that
// lives in vendor-side billing alerts — but it gives a numeric signal
// that lands in CI logs, so drift (a regression that suddenly makes
// 100× more API calls) is visible.
var operationCount int64

func recordOp(_ *testing.T) {
	atomic.AddInt64(&operationCount, 1)
}

// TestZZZ_OperationCountSummary runs last (Go test ordering is
// alphabetical within a package; the ZZZ prefix guarantees it). Prints
// the aggregate API operation count for the whole run.
//
// This is informational. The test always passes. CI captures the log
// line with a grep in the workflow for trend-line dashboards.
func TestZZZ_OperationCountSummary(t *testing.T) {
	n := atomic.LoadInt64(&operationCount)
	t.Logf("Wave 3 operation count this run: %d", n)
	// Intentionally no upper bound assertion. Cost control is vendor-side.
}
