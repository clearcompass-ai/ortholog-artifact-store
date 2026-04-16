package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/clearcompass-ai/ortholog-artifact-store/config"
	"go.uber.org/goleak"
)

// TestMain guards against goroutine leaks from the watchdog loop. If a
// test calls runVerifyOnPushWatchdog and fails to cancel the context,
// goleak surfaces the leak at binary exit.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// ─── AS-1 tests ──────────────────────────────────────────────────────

// safeBuffer is a concurrency-safe wrapper. slog.TextHandler writes from
// the watchdog goroutine; bytes.Buffer is not safe for concurrent use.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
func (b *safeBuffer) CountOccurrences(substr string) int {
	return strings.Count(b.String(), substr)
}

// newWatchdogLogger returns a logger backed by a concurrency-safe buffer.
func newWatchdogLogger() (*slog.Logger, *safeBuffer) {
	buf := &safeBuffer{}
	h := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	return slog.New(h), buf
}

// TestWatchdog_Silent_WhenVerifyOnPushEnabled is the happy path: the
// config is correct, no warning should ever appear in the log stream.
func TestWatchdog_Silent_WhenVerifyOnPushEnabled(t *testing.T) {
	logger, buf := newWatchdogLogger()
	cfg := &config.Config{VerifyOnPush: true, Env: "production"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runVerifyOnPushWatchdog(ctx, cfg, logger)

	if got := buf.String(); strings.Contains(got, "VerifyOnPush is disabled") {
		t.Fatalf("watchdog fired a warning when VerifyOnPush=true; got:\n%s", got)
	}
}

// TestWatchdog_OneShot_InDev confirms that dev/staging emits exactly
// one warning at startup and does NOT start a ticker. Disabling
// verification in dev is often legitimate and repeating the warning
// every minute would just train operators to ignore it.
func TestWatchdog_OneShot_InDev(t *testing.T) {
	// Shorten the tick interval so that if the watchdog mistakenly
	// starts a ticker, we'd see a second warning within the test window.
	orig := verifyWarnInterval
	verifyWarnInterval = 10 * time.Millisecond
	t.Cleanup(func() { verifyWarnInterval = orig })

	logger, buf := newWatchdogLogger()
	cfg := &config.Config{VerifyOnPush: false, Env: "dev"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runVerifyOnPushWatchdog(ctx, cfg, logger)

	// Wait long enough for several ticks to pass if one existed.
	time.Sleep(80 * time.Millisecond)

	count := buf.CountOccurrences("VerifyOnPush is disabled")
	if count != 1 {
		t.Fatalf("dev env: want 1 warning, got %d; log:\n%s", count, buf.String())
	}
	if !strings.Contains(buf.String(), "artifact.config.verify_on_push_disabled") {
		t.Fatalf("warning missing event attr; log:\n%s", buf.String())
	}
}

// TestWatchdog_Periodic_InProduction is the core AS-1 property: when
// VerifyOnPush=false AND env=production, the warning repeats on a
// ticker. We override verifyWarnInterval to 10ms so the test runs fast.
func TestWatchdog_Periodic_InProduction(t *testing.T) {
	orig := verifyWarnInterval
	verifyWarnInterval = 10 * time.Millisecond
	t.Cleanup(func() { verifyWarnInterval = orig })

	logger, buf := newWatchdogLogger()
	cfg := &config.Config{VerifyOnPush: false, Env: "production"}

	ctx, cancel := context.WithCancel(context.Background())
	runVerifyOnPushWatchdog(ctx, cfg, logger)

	// Wait for roughly 5 ticks: startup + 4 periodic (plus scheduling slack).
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if buf.CountOccurrences("VerifyOnPush is disabled") >= 3 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Cancel cleanly so goleak sees no survivor.
	cancel()
	time.Sleep(30 * time.Millisecond) // allow goroutine to observe ctx.Done

	count := buf.CountOccurrences("VerifyOnPush is disabled")
	if count < 3 {
		t.Fatalf("production env: want ≥3 warnings, got %d; log:\n%s", count, buf.String())
	}

	// Every warning must carry the structured event + env attributes.
	log := buf.String()
	if !strings.Contains(log, "artifact.config.verify_on_push_disabled") {
		t.Fatalf("warning missing event attr; log:\n%s", log)
	}
	if !strings.Contains(log, "env=production") {
		t.Fatalf("warning missing env=production attr; log:\n%s", log)
	}
}

// TestWatchdog_CancelStopsTicker verifies the ticker goroutine shuts
// down when the context is cancelled. goleak in TestMain would catch
// a surviving goroutine regardless, but an explicit observation here
// makes the contract visible.
func TestWatchdog_CancelStopsTicker(t *testing.T) {
	orig := verifyWarnInterval
	verifyWarnInterval = 5 * time.Millisecond
	t.Cleanup(func() { verifyWarnInterval = orig })

	logger, buf := newWatchdogLogger()
	cfg := &config.Config{VerifyOnPush: false, Env: "production"}

	ctx, cancel := context.WithCancel(context.Background())
	runVerifyOnPushWatchdog(ctx, cfg, logger)

	// Let it tick a few times.
	time.Sleep(50 * time.Millisecond)

	cancel()
	// Give the goroutine a moment to observe cancellation.
	time.Sleep(20 * time.Millisecond)

	countAtCancel := buf.CountOccurrences("VerifyOnPush is disabled")

	// After cancellation, more ticks' worth of time passes. Warning
	// count must be stable (no new ticks emitted).
	time.Sleep(80 * time.Millisecond)
	countAfter := buf.CountOccurrences("VerifyOnPush is disabled")

	if countAfter != countAtCancel {
		t.Fatalf("ticker kept firing after cancel: %d → %d", countAtCancel, countAfter)
	}
}
