// Package testutil provides shared test infrastructure for the artifact store.
//
// This package is internal (not importable from outside this module). It
// contains the slog capturing handler, reusable httptest server builders
// for cloud providers, goleak integration, known CID vectors, and
// deterministic random data generators used across unit and conformance
// tests.
package testutil

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// SlogCapture is a slog.Handler that records every log record into a
// mutex-guarded slice. Tests inject it via slog.New(capture) to assert
// that handlers emitted the expected audit warnings.
//
// Records are snapshot-copied on read so assertions don't race with
// concurrent producers (the HTTP handlers under test).
type SlogCapture struct {
	mu      sync.Mutex
	records []CapturedRecord
}

// CapturedRecord is a point-in-time snapshot of an slog.Record.
type CapturedRecord struct {
	Level   slog.Level
	Message string
	Attrs   map[string]any
}

// NewSlogCapture returns a capturing handler ready to embed in slog.New.
func NewSlogCapture() *SlogCapture {
	return &SlogCapture{}
}

// Enabled returns true for all levels — tests care about every record.
func (c *SlogCapture) Enabled(_ context.Context, _ slog.Level) bool {
	return true
}

// Handle snapshots the record and its attributes into the internal slice.
func (c *SlogCapture) Handle(_ context.Context, r slog.Record) error {
	rec := CapturedRecord{
		Level:   r.Level,
		Message: r.Message,
		Attrs:   make(map[string]any, r.NumAttrs()),
	}
	r.Attrs(func(a slog.Attr) bool {
		rec.Attrs[a.Key] = a.Value.Any()
		return true
	})
	c.mu.Lock()
	c.records = append(c.records, rec)
	c.mu.Unlock()
	return nil
}

// WithAttrs / WithGroup are unused in the tests we write — we emit attrs
// inline via logger.Warn(..., "key", value). Implementing them as identity
// returns keeps us slog.Handler-compliant without attribute merging.
func (c *SlogCapture) WithAttrs(_ []slog.Attr) slog.Handler { return c }
func (c *SlogCapture) WithGroup(_ string) slog.Handler      { return c }

// Records returns a snapshot of all captured records. Safe to call
// concurrently with Handle; the returned slice is owned by the caller.
func (c *SlogCapture) Records() []CapturedRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]CapturedRecord, len(c.records))
	copy(out, c.records)
	return out
}

// Len returns the count of captured records.
func (c *SlogCapture) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.records)
}

// Reset clears all captured records. Useful between sub-tests.
func (c *SlogCapture) Reset() {
	c.mu.Lock()
	c.records = nil
	c.mu.Unlock()
}

// Logger returns a *slog.Logger that writes to this capture handler.
func (c *SlogCapture) Logger() *slog.Logger {
	return slog.New(c)
}

// AssertContains fails the test if no captured record has the given
// level + message substring AND all of the expected attribute key/value
// pairs. Attribute values are compared with fmt.Sprintf("%v", ...) so
// tests can pass any type (int, string, etc.) without type juggling.
//
// Example:
//
//	cap.AssertContains(t, slog.LevelWarn, "push rejected: body exceeds",
//	    map[string]any{"cid": "sha256:...", "received_size": 1025})
func (c *SlogCapture) AssertContains(t *testing.T, level slog.Level, msgSubstr string, wantAttrs map[string]any) {
	t.Helper()
	records := c.Records()
	for _, r := range records {
		if r.Level != level {
			continue
		}
		if !strings.Contains(r.Message, msgSubstr) {
			continue
		}
		if attrsMatch(r.Attrs, wantAttrs) {
			return // found
		}
	}
	t.Fatalf("no captured record matched level=%s msg~=%q attrs=%v\nrecords:\n%s",
		level, msgSubstr, wantAttrs, formatRecords(records))
}

// AssertNoWarnings fails if any captured record is at slog.LevelWarn or
// higher. Used for happy-path tests that must emit no audit warnings.
func (c *SlogCapture) AssertNoWarnings(t *testing.T) {
	t.Helper()
	for _, r := range c.Records() {
		if r.Level >= slog.LevelWarn {
			t.Fatalf("unexpected warn/error record: level=%s msg=%q attrs=%v",
				r.Level, r.Message, r.Attrs)
		}
	}
}

func attrsMatch(got, want map[string]any) bool {
	for k, wv := range want {
		gv, ok := got[k]
		if !ok {
			return false
		}
		if fmt.Sprintf("%v", gv) != fmt.Sprintf("%v", wv) {
			return false
		}
	}
	return true
}

func formatRecords(records []CapturedRecord) string {
	var b strings.Builder
	for i, r := range records {
		fmt.Fprintf(&b, "  [%d] level=%s msg=%q attrs=%v\n", i, r.Level, r.Message, r.Attrs)
	}
	if b.Len() == 0 {
		return "  (none)\n"
	}
	return b.String()
}
