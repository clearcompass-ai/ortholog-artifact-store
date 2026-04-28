//go:build staging

package staging

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/clearcompass-ai/ortholog-artifact-store/api"
	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// TestScale_GCS_PushFetch is the standalone load test against real GCS.
//
// It exercises the full push → fetch → delete path through an
// in-process api.NewMux server wired to a real GCSBackend, the same
// way production does. The body bytes never leave the test process
// except over the wire to GCS, so this measures the artifact store's
// own overhead plus the GCS round-trip — exactly the production cost.
//
// Workload: SCALE_N objects (default 10_000) at SCALE_CONCURRENCY
// workers (default 64), payload size cycling 1 KiB → 64 KiB → 1 MiB
// → 1 KiB … so the run touches the small-object, medium-object, and
// near-MaxBodySize regimes evenly. Total bytes pushed ≈ N * 363 KiB
// (3.6 GiB at the default).
//
// VerifyOnPush is left enabled (production-realistic): the server
// SHA-256s every body before persisting.
//
// Read-back: a SCALE_READBACK_PCT (default 1) sample is fetched and
// the SHA-256 of the response is checked against the CID, proving
// the bytes survived the round-trip end-to-end. Full read-back of a
// 10K-object run with mixed sizes adds ~3.6 GiB of egress; the 1 %
// sample keeps cost bounded while still catching wholesale corruption.
//
// Cleanup: every successfully-pushed CID is deleted at the end via
// the backend directly. Failures are counted but do not fail the
// test — the bucket-level lifecycle rule on the staging/ prefix
// reaps anything we miss after 24 h.
//
// Reporting: throughput (obj/s, MiB/s) and push-latency p50/p95/p99
// per phase, logged via t.Logf so the numbers land in the CI summary
// for trend-line dashboards.
func TestScale_GCS_PushFetch(t *testing.T) {
	if !gcsConfigured() {
		t.Skip("GCS not configured")
	}

	n := envIntDefault(t, "SCALE_N", 10_000)
	concurrency := envIntDefault(t, "SCALE_CONCURRENCY", 64)
	readbackPct := envIntDefault(t, "SCALE_READBACK_PCT", 1)
	if n < 1 {
		t.Fatalf("SCALE_N must be ≥ 1, got %d", n)
	}
	if concurrency < 1 {
		t.Fatalf("SCALE_CONCURRENCY must be ≥ 1, got %d", concurrency)
	}
	if readbackPct < 0 || readbackPct > 100 {
		t.Fatalf("SCALE_READBACK_PCT must be in [0,100], got %d", readbackPct)
	}

	// Mixed-size distribution: 1 KiB / 64 KiB / 1 MiB, cycled.
	sizes := []int{1 << 10, 64 << 10, 1 << 20}

	prefix := randomPrefix(t)
	backend := newGCSBackend(t, prefix)
	t.Logf("scale: N=%d concurrency=%d readback_pct=%d prefix=%s",
		n, concurrency, readbackPct, prefix)

	// In-process server wired to the real GCS backend. httptest.Server
	// gives us a real loopback HTTP path — that's the bit production
	// clients hit. The backend reaches real GCS over the network.
	handler := api.NewMux(api.ServerConfig{
		Backend:       backend,
		VerifyOnPush:  true,
		MaxBodySize:   64 << 20,
		DefaultExpiry: time.Hour,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// Pre-compute every CID so the readback phase can address them
	// without re-deriving bodies from PRNG seeds. Bodies themselves
	// are NOT held — they're regenerated per worker to keep peak
	// RSS bounded (a 10K * 1MiB pre-allocation would be 10 GiB).
	cids := make([]storage.CID, n)
	pushLatencies := make([]time.Duration, n)
	pushErrs := make([]error, n)
	var totalBytes int64

	// ── Phase 1: push ─────────────────────────────────────────────
	t.Logf("phase 1: push")
	jobs := make(chan int, concurrency*2)
	go func() {
		for i := 0; i < n; i++ {
			jobs <- i
		}
		close(jobs)
	}()

	var pushed int64
	pushStart := time.Now()
	httpClient := &http.Client{Timeout: 5 * time.Minute}

	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				size := sizes[i%len(sizes)]
				body := makeScaleBody(i, size)
				cid := storage.Compute(body)
				cids[i] = cid

				req, err := http.NewRequest(http.MethodPost,
					srv.URL+"/v1/artifacts", bytes.NewReader(body))
				if err != nil {
					pushErrs[i] = err
					continue
				}
				req.Header.Set("X-Artifact-CID", cid.String())
				req.Header.Set("Content-Type", "application/octet-stream")
				req.ContentLength = int64(len(body))

				start := time.Now()
				resp, err := httpClient.Do(req)
				pushLatencies[i] = time.Since(start)
				if err != nil {
					pushErrs[i] = err
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					pushErrs[i] = fmt.Errorf("status %d", resp.StatusCode)
					continue
				}
				atomic.AddInt64(&totalBytes, int64(len(body)))
				recordOp(t)

				np := atomic.AddInt64(&pushed, 1)
				if np%1000 == 0 {
					elapsed := time.Since(pushStart)
					t.Logf("  push %d/%d (%.0f obj/s, %s elapsed)",
						np, n, float64(np)/elapsed.Seconds(),
						elapsed.Round(time.Second))
				}
			}
		}()
	}
	wg.Wait()
	pushElapsed := time.Since(pushStart)

	pushOK := 0
	for _, e := range pushErrs {
		if e == nil {
			pushOK++
		}
	}
	pushFailed := n - pushOK
	mibPushed := float64(atomic.LoadInt64(&totalBytes)) / (1 << 20)
	t.Logf("PUSH: %d ok / %d failed in %s (%.0f obj/s, %.1f MiB/s)",
		pushOK, pushFailed, pushElapsed.Round(time.Millisecond),
		float64(pushOK)/pushElapsed.Seconds(),
		mibPushed/pushElapsed.Seconds())
	logPercentiles(t, "push", pushLatencies, pushErrs)

	if pushFailed > 0 {
		// Surface the first error verbatim for triage.
		for i, e := range pushErrs {
			if e != nil {
				t.Errorf("first push failure (idx %d): %v", i, e)
				break
			}
		}
	}

	// ── Phase 2: read-back sample ────────────────────────────────
	if readbackPct > 0 && pushOK > 0 {
		sampleN := pushOK * readbackPct / 100
		if sampleN < 1 {
			sampleN = 1
		}
		t.Logf("phase 2: readback (%d of %d, %d%%)", sampleN, pushOK, readbackPct)

		// Pick a deterministic stride sample over indices that pushed OK.
		okIdx := make([]int, 0, pushOK)
		for i, e := range pushErrs {
			if e == nil {
				okIdx = append(okIdx, i)
			}
		}
		stride := len(okIdx) / sampleN
		if stride < 1 {
			stride = 1
		}
		fetchIdx := make([]int, 0, sampleN)
		for k := 0; k < len(okIdx) && len(fetchIdx) < sampleN; k += stride {
			fetchIdx = append(fetchIdx, okIdx[k])
		}

		fetchLatencies := make([]time.Duration, len(fetchIdx))
		fetchErrs := make([]error, len(fetchIdx))

		fetchJobs := make(chan int, concurrency*2)
		go func() {
			for j := range fetchIdx {
				fetchJobs <- j
			}
			close(fetchJobs)
		}()

		var fetchedBytes int64
		fetchStart := time.Now()
		var wgF sync.WaitGroup
		for w := 0; w < concurrency; w++ {
			wgF.Add(1)
			go func() {
				defer wgF.Done()
				for j := range fetchJobs {
					i := fetchIdx[j]
					cid := cids[i]
					expectedSize := sizes[i%len(sizes)]

					start := time.Now()
					resp, err := httpClient.Get(
						srv.URL + "/v1/artifacts/" + cid.String())
					if err != nil {
						fetchLatencies[j] = time.Since(start)
						fetchErrs[j] = err
						continue
					}
					h := sha256.New()
					nRead, err := io.Copy(h, resp.Body)
					_ = resp.Body.Close()
					fetchLatencies[j] = time.Since(start)
					if err != nil {
						fetchErrs[j] = err
						continue
					}
					if resp.StatusCode != http.StatusOK {
						fetchErrs[j] = fmt.Errorf("status %d", resp.StatusCode)
						continue
					}
					if nRead != int64(expectedSize) {
						fetchErrs[j] = fmt.Errorf("size mismatch: got %d want %d",
							nRead, expectedSize)
						continue
					}
					if !bytes.Equal(h.Sum(nil), cid.Digest) {
						fetchErrs[j] = fmt.Errorf("digest mismatch on cid %s", cid)
						continue
					}
					atomic.AddInt64(&fetchedBytes, nRead)
					recordOp(t)
				}
			}()
		}
		wgF.Wait()
		fetchElapsed := time.Since(fetchStart)

		fetchOK := 0
		for _, e := range fetchErrs {
			if e == nil {
				fetchOK++
			}
		}
		fetchFailed := len(fetchIdx) - fetchOK
		t.Logf("FETCH: %d ok / %d failed in %s (%.0f obj/s, %.1f MiB/s)",
			fetchOK, fetchFailed, fetchElapsed.Round(time.Millisecond),
			float64(fetchOK)/fetchElapsed.Seconds(),
			float64(atomic.LoadInt64(&fetchedBytes))/(1<<20)/fetchElapsed.Seconds())
		logPercentiles(t, "fetch", fetchLatencies, fetchErrs)

		if fetchFailed > 0 {
			for j, e := range fetchErrs {
				if e != nil {
					t.Errorf("first fetch failure (sample %d, push idx %d): %v",
						j, fetchIdx[j], e)
					break
				}
			}
		}
	}

	// ── Phase 3: cleanup (delete every CID we successfully pushed) ─
	t.Logf("phase 3: cleanup")
	cleanupStart := time.Now()
	var cleanupErrs int64
	var deleted int64

	cleanJobs := make(chan int, concurrency*2)
	go func() {
		for i := 0; i < n; i++ {
			if pushErrs[i] == nil {
				cleanJobs <- i
			}
		}
		close(cleanJobs)
	}()

	var wgC sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wgC.Add(1)
		go func() {
			defer wgC.Done()
			for i := range cleanJobs {
				if err := backend.Delete(cids[i]); err != nil {
					atomic.AddInt64(&cleanupErrs, 1)
					continue
				}
				atomic.AddInt64(&deleted, 1)
				recordOp(t)
			}
		}()
	}
	wgC.Wait()
	cleanupElapsed := time.Since(cleanupStart)

	t.Logf("CLEANUP: %d deleted / %d errors in %s (%.0f obj/s)",
		atomic.LoadInt64(&deleted), atomic.LoadInt64(&cleanupErrs),
		cleanupElapsed.Round(time.Millisecond),
		float64(atomic.LoadInt64(&deleted))/cleanupElapsed.Seconds())
	if atomic.LoadInt64(&cleanupErrs) > 0 {
		// The bucket lifecycle rule on staging/ will reap leftovers
		// after 24 h, so we don't fail the test here.
		t.Logf("note: cleanup errors are tolerated; bucket lifecycle reaps stragglers")
	}

	// Sanity: backend interface compile-check.
	var _ backends.BackendProvider = backend
}

// makeScaleBody returns a deterministic byte slice of the requested
// size, seeded by index. Deterministic so a failed CID can be
// reproduced from its index alone — the test logs the index of every
// failure.
func makeScaleBody(idx, size int) []byte {
	buf := make([]byte, size)
	// 0x9E3779B97F4A7C15 is the 64-bit golden-ratio constant; cast
	// through uint64 since the literal exceeds int64.
	r := rand.New(rand.NewSource(int64(uint64(idx)*0x9E3779B97F4A7C15 + 1)))
	_, _ = r.Read(buf)
	return buf
}

// envIntDefault reads an env var as int, falling back to def. Logs
// the resolved value via t.Logf for run reproducibility.
func envIntDefault(t *testing.T, key string, def int) int {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		t.Fatalf("env %s: not an int: %q (%v)", key, v, err)
	}
	return n
}

// logPercentiles emits p50/p95/p99/max over successful (err == nil)
// observations. Errors have latency too (they were observed) but
// mixing them skews the tail toward timeouts; we report only OKs and
// mention the error count separately.
func logPercentiles(t *testing.T, label string, latencies []time.Duration, errs []error) {
	t.Helper()
	ok := make([]time.Duration, 0, len(latencies))
	for i, lat := range latencies {
		if i < len(errs) && errs[i] != nil {
			continue
		}
		ok = append(ok, lat)
	}
	if len(ok) == 0 {
		t.Logf("  %s latency: no successful observations", label)
		return
	}
	sort.Slice(ok, func(a, b int) bool { return ok[a] < ok[b] })
	p := func(q float64) time.Duration {
		idx := int(float64(len(ok)-1) * q)
		return ok[idx]
	}
	t.Logf("  %s latency p50=%s p95=%s p99=%s max=%s (n=%d)",
		label,
		p(0.50).Round(time.Millisecond),
		p(0.95).Round(time.Millisecond),
		p(0.99).Round(time.Millisecond),
		ok[len(ok)-1].Round(time.Millisecond),
		len(ok))
}
