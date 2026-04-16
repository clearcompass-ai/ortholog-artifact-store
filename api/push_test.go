package api

import (
	"bytes"
	"crypto/sha256"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-artifact-store/internal/testutil"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// newTestPush builds a PushHandler backed by an InMemoryBackend and a
// capturing logger. Returns the handler, the backend (for state assertions),
// and the capture (for audit-log assertions).
func newTestPush(t *testing.T, verify bool, maxBody int64) (*PushHandler, *backends.InMemoryBackend, *testutil.SlogCapture) {
	t.Helper()
	cap := testutil.NewSlogCapture()
	b := backends.NewInMemoryBackend()
	h := &PushHandler{
		backend: b,
		verify:  verify,
		maxBody: maxBody,
		logger:  cap.Logger(),
	}
	return h, b, cap
}

// doPush executes one request against the handler and returns the recorder.
// cidHeader is the value of X-Artifact-CID; pass "" to omit the header.
func doPush(h *PushHandler, cidHeader string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/artifacts", bytes.NewReader(body))
	req.RemoteAddr = "192.0.2.1:12345" // documentation IP, asserted in audit tests
	if cidHeader != "" {
		req.Header.Set("X-Artifact-CID", cidHeader)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// ─── Happy path ──────────────────────────────────────────────────────

func TestPush_HappyPath_VerifyOn(t *testing.T) {
	data := []byte("happy-path-payload")
	cid := storage.Compute(data)
	h, b, cap := newTestPush(t, true, 1024)

	w := doPush(h, cid.String(), data)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d; body=%s", w.Code, w.Body.String())
	}
	// Backend should now hold the bytes.
	got, err := b.Fetch(cid)
	if err != nil {
		t.Fatalf("Fetch after Push: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("backend bytes differ from pushed bytes")
	}
	cap.AssertNoWarnings(t)
}

func TestPush_HappyPath_VerifyOff_Mismatch(t *testing.T) {
	// With verify off, the handler must accept bytes that do not match the CID.
	// This is the intended (dangerous) behavior when VerifyOnPush=false.
	claimedCID := storage.Compute([]byte("original"))
	actualBody := []byte("something-else-entirely")
	h, b, cap := newTestPush(t, false /* verify */, 1024)

	w := doPush(h, claimedCID.String(), actualBody)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200 (verify off accepts anything), got %d; body=%s", w.Code, w.Body.String())
	}
	// The backend now contains the actualBody bytes under the claimedCID.
	// This is the data-corruption scenario the startup warning exists to prevent.
	got, err := b.Fetch(claimedCID)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !bytes.Equal(got, actualBody) {
		t.Fatal("backend should have stored the mismatched bytes under the claimed CID")
	}
	cap.AssertNoWarnings(t) // verify=false skips the audit warning entirely
}

// ─── Missing/malformed CID header ────────────────────────────────────

func TestPush_MissingCIDHeader(t *testing.T) {
	h, _, _ := newTestPush(t, true, 1024)
	w := doPush(h, "", []byte("payload"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "X-Artifact-CID") {
		t.Fatalf("error body missing expected substring: %s", w.Body.String())
	}
}

func TestPush_MalformedCID(t *testing.T) {
	h, _, _ := newTestPush(t, true, 1024)
	w := doPush(h, "not-a-valid-cid-format", []byte("payload"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid CID") {
		t.Fatalf("error body missing expected substring: %s", w.Body.String())
	}
}

// ─── Size limit (3.6 audit trail) ────────────────────────────────────

func TestPush_BodyAtLimit_Accepted(t *testing.T) {
	// Body exactly MaxBodySize should be accepted.
	const limit = 256
	data := testutil.DeterministicBytes(1, limit)
	cid := storage.Compute(data)
	h, _, cap := newTestPush(t, true, limit)

	w := doPush(h, cid.String(), data)
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200 for body exactly at limit, got %d", w.Code)
	}
	cap.AssertNoWarnings(t)
}

func TestPush_BodyOverLimit_RejectedWithAudit(t *testing.T) {
	// Body one byte over MaxBodySize must be rejected with 413 AND logged.
	const limit = 256
	data := testutil.DeterministicBytes(1, limit+1)
	cid := storage.Compute(data)
	h, b, cap := newTestPush(t, true, limit)

	w := doPush(h, cid.String(), data)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: want 413, got %d; body=%s", w.Code, w.Body.String())
	}

	// Audit log must contain the event/reason pair plus context attrs.
	cap.AssertContains(t, slog.LevelWarn, "push rejected: body exceeds size limit",
		map[string]any{
			"event":         "artifact.push.rejected",
			"reason":        "size_exceeded",
			"claimed_cid":   cid.String(),
			"remote_addr":   "192.0.2.1:12345",
			"max_body_size": int64(limit),
		})

	// Nothing should be in the backend.
	exists, err := b.Exists(cid)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Fatal("over-limit body somehow reached the backend")
	}
}

// ─── Digest mismatch (3.6 audit trail) ───────────────────────────────

func TestPush_DigestMismatch_RejectedWithAudit(t *testing.T) {
	// Claim a CID for one payload, send different bytes. With verify=true,
	// this must be rejected with 400 AND logged with both digests.
	claimedCID := storage.Compute([]byte("the-real-original"))
	corruptedBody := []byte("tampered-bytes")
	h, b, cap := newTestPush(t, true, 1024)

	w := doPush(h, claimedCID.String(), corruptedBody)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d", w.Code)
	}

	// Audit log must carry event/reason plus both digests.
	computed := sha256.Sum256(corruptedBody)
	cap.AssertContains(t, slog.LevelWarn, "push rejected: SHA-256 digest mismatch",
		map[string]any{
			"event":           "artifact.push.rejected",
			"reason":          "cid_mismatch",
			"claimed_cid":     claimedCID.String(),
			"remote_addr":     "192.0.2.1:12345",
			"received_size":   int64(len(corruptedBody)),
			"computed_digest": hexDigest(computed[:]),
			"claimed_digest":  hexDigest(claimedCID.Digest),
		})

	// Nothing should be stored.
	exists, err := b.Exists(claimedCID)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Fatal("mismatched body was stored despite rejection")
	}
}

// ─── Body read errors ────────────────────────────────────────────────

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestPush_BodyReadError(t *testing.T) {
	h, _, _ := newTestPush(t, true, 1024)
	req := httptest.NewRequest(http.MethodPost, "/v1/artifacts", io.NopCloser(failingReader{}))
	req.Header.Set("X-Artifact-CID", storage.Compute([]byte("x")).String())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "read body") {
		t.Fatalf("error body missing expected substring: %s", w.Body.String())
	}
}

// ─── Backend error propagation ───────────────────────────────────────

// failingPushBackend satisfies BackendProvider but fails every Push.
// Wraps InMemoryBackend to satisfy the rest of the interface trivially.
type failingPushBackend struct {
	backends.BackendProvider
}

func (f failingPushBackend) Push(_ storage.CID, _ []byte) error {
	return storage.ErrNotSupported
}

func TestPush_BackendError_Returns500(t *testing.T) {
	cap := testutil.NewSlogCapture()
	h := &PushHandler{
		backend: failingPushBackend{BackendProvider: backends.NewInMemoryBackend()},
		verify:  true,
		maxBody: 1024,
		logger:  cap.Logger(),
	}
	data := []byte("will-fail")
	w := doPush(h, storage.Compute(data).String(), data)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: want 500, got %d", w.Code)
	}
}

// ─── Empty body ──────────────────────────────────────────────────────

func TestPush_EmptyBody(t *testing.T) {
	empty := []byte{}
	cid := storage.Compute(empty)
	h, b, cap := newTestPush(t, true, 1024)

	w := doPush(h, cid.String(), empty)
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200 for empty body (CID valid), got %d", w.Code)
	}
	got, err := b.Fetch(cid)
	if err != nil {
		t.Fatalf("Fetch empty: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty body round-trip failed: len=%d", len(got))
	}
	cap.AssertNoWarnings(t)
}

// ─── Helpers ─────────────────────────────────────────────────────────

// hexDigest mirrors encoding/hex.EncodeToString without importing in the test.
func hexDigest(b []byte) string {
	const hexchars = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexchars[v>>4]
		out[i*2+1] = hexchars[v&0x0f]
	}
	return string(out)
}
