package api

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-artifact-store/internal/testutil"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// ─── Helpers ────────────────────────────────────────────────────────

// newTestPushWithToken builds a PushHandler wired with a real Ed25519
// verifier, returning the private key the test can use to mint tokens.
func newTestPushWithToken(t *testing.T, policy string) (
	*PushHandler, *backends.InMemoryBackend, *testutil.SlogCapture, ed25519.PrivateKey,
) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	cap := testutil.NewSlogCapture()
	b := backends.NewInMemoryBackend()
	h := &PushHandler{
		backend:       b,
		verify:        true,
		maxBody:       1024,
		logger:        cap.Logger(),
		tokenVerifier: NewEd25519UploadTokenVerifier(pub),
		tokenPolicy:   policy,
	}
	return h, b, cap, priv
}

// doPushWithToken executes a push request with an X-Upload-Token header.
func doPushWithToken(h *PushHandler, cid, token string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/artifacts", bytes.NewReader(body))
	req.RemoteAddr = "192.0.2.1:12345"
	req.Header.Set("X-Artifact-CID", cid)
	if token != "" {
		req.Header.Set("X-Upload-Token", token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// ─── Policy: required ────────────────────────────────────────────────

func TestPush_TokenRequired_MissingRejected(t *testing.T) {
	h, _, cap, _ := newTestPushWithToken(t, "required")

	data := []byte("requires-token")
	cid := storage.Compute(data)
	w := doPushWithToken(h, cid.String(), "", data)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: want 401, got %d", w.Code)
	}
	cap.AssertContains(t, slog.LevelWarn, "upload token required but missing",
		map[string]any{
			"event":       "artifact.push.rejected",
			"reason":      "token_required_missing",
			"claimed_cid": cid.String(),
			"remote_addr": "192.0.2.1:12345",
		})
}

func TestPush_TokenRequired_ValidAccepted(t *testing.T) {
	h, b, cap, priv := newTestPushWithToken(t, "required")

	data := []byte("accepted-with-token")
	cid := storage.Compute(data)
	tok := mintToken(t, priv, UploadTokenPayload{
		CID:  cid.String(),
		Size: int64(len(data)),
		Exp:  time.Now().Unix() + 60,
		Kid:  "op-key-A",
	})

	w := doPushWithToken(h, cid.String(), tok, data)
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d; body=%s", w.Code, w.Body.String())
	}

	exists, err := b.Exists(cid)
	if err != nil || !exists {
		t.Fatal("data not stored after valid token push")
	}
	cap.AssertNoWarnings(t)
}

func TestPush_TokenRequired_BadSignatureRejected(t *testing.T) {
	h, _, cap, _ := newTestPushWithToken(t, "required")

	// Sign with an attacker's key, not the trusted one.
	_, attackerPriv, _ := ed25519.GenerateKey(rand.Reader)
	data := []byte("attacker-bytes")
	cid := storage.Compute(data)
	tok := mintToken(t, attackerPriv, UploadTokenPayload{
		CID:  cid.String(),
		Size: int64(len(data)),
		Exp:  time.Now().Unix() + 60,
	})

	w := doPushWithToken(h, cid.String(), tok, data)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: want 401, got %d", w.Code)
	}
	cap.AssertContains(t, slog.LevelWarn, "upload token failed",
		map[string]any{
			"event":  "artifact.push.rejected",
			"reason": "token_invalid",
		})
}

func TestPush_TokenRequired_ExpiredRejected(t *testing.T) {
	h, _, cap, priv := newTestPushWithToken(t, "required")

	data := []byte("stale-token")
	cid := storage.Compute(data)
	tok := mintToken(t, priv, UploadTokenPayload{
		CID:  cid.String(),
		Size: int64(len(data)),
		Exp:  time.Now().Unix() - 1, // already expired
	})

	w := doPushWithToken(h, cid.String(), tok, data)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: want 401, got %d", w.Code)
	}
	cap.AssertContains(t, slog.LevelWarn, "upload token failed",
		map[string]any{
			"event":  "artifact.push.rejected",
			"reason": "token_expired",
		})
}

func TestPush_TokenRequired_CIDMismatchRejected(t *testing.T) {
	h, _, cap, priv := newTestPushWithToken(t, "required")

	// Attacker obtains a valid token for CID-A but tries to push CID-B.
	// The header says CID-B, the token binds CID-A → rejected.
	realData := []byte("real")
	otherData := []byte("something-else")
	realCID := storage.Compute(realData)
	otherCID := storage.Compute(otherData)
	tok := mintToken(t, priv, UploadTokenPayload{
		CID:  realCID.String(),
		Size: int64(len(realData)),
		Exp:  time.Now().Unix() + 60,
	})

	// Push with mismatched header CID and body.
	w := doPushWithToken(h, otherCID.String(), tok, otherData)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: want 401, got %d", w.Code)
	}
	cap.AssertContains(t, slog.LevelWarn, "upload token failed",
		map[string]any{
			"event":  "artifact.push.rejected",
			"reason": "token_cid_mismatch",
		})
}

func TestPush_TokenRequired_SizeMismatchRejected(t *testing.T) {
	h, _, cap, priv := newTestPushWithToken(t, "required")

	// Token claims size=1000 but the actual body is much smaller.
	// Defends against truncation attacks where an attacker shrinks a
	// pre-authorized payload to a different but same-CID-prefix value.
	data := []byte("small-body")
	cid := storage.Compute(data)
	tok := mintToken(t, priv, UploadTokenPayload{
		CID:  cid.String(),
		Size: 1000, // wrong
		Exp:  time.Now().Unix() + 60,
	})

	w := doPushWithToken(h, cid.String(), tok, data)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: want 401, got %d", w.Code)
	}
	cap.AssertContains(t, slog.LevelWarn, "upload token failed",
		map[string]any{
			"event":  "artifact.push.rejected",
			"reason": "token_size_mismatch",
		})
}

// ─── Policy: optional ────────────────────────────────────────────────

func TestPush_TokenOptional_MissingAccepted(t *testing.T) {
	// With policy=optional and no token, push should succeed. This is
	// the rollout path: flip policy to "optional" first, issue tokens to
	// a subset of clients, observe, then escalate to "required".
	h, b, cap, _ := newTestPushWithToken(t, "optional")

	data := []byte("no-token-but-allowed")
	cid := storage.Compute(data)
	w := doPushWithToken(h, cid.String(), "", data)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d; body=%s", w.Code, w.Body.String())
	}
	exists, _ := b.Exists(cid)
	if !exists {
		t.Fatal("data not stored on optional+missing")
	}
	cap.AssertNoWarnings(t)
}

func TestPush_TokenOptional_InvalidRejected(t *testing.T) {
	// With policy=optional, a present-but-invalid token is still rejected —
	// otherwise the server would silently accept forged tokens and obscure
	// detection. "Present" is a commitment.
	h, _, cap, _ := newTestPushWithToken(t, "optional")

	_, attackerPriv, _ := ed25519.GenerateKey(rand.Reader)
	data := []byte("forged")
	cid := storage.Compute(data)
	tok := mintToken(t, attackerPriv, UploadTokenPayload{
		CID:  cid.String(),
		Size: int64(len(data)),
		Exp:  time.Now().Unix() + 60,
	})

	w := doPushWithToken(h, cid.String(), tok, data)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: want 401, got %d", w.Code)
	}
	cap.AssertContains(t, slog.LevelWarn, "upload token failed",
		map[string]any{
			"event":  "artifact.push.rejected",
			"reason": "token_invalid",
		})
}

// ─── Policy: off ─────────────────────────────────────────────────────

func TestPush_TokenOff_IgnoresHeader(t *testing.T) {
	// With policy=off, even a syntactically invalid token must be ignored.
	// This is the baseline deployment mode: the store trusts network
	// segmentation entirely.
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	cap := testutil.NewSlogCapture()
	b := backends.NewInMemoryBackend()
	h := &PushHandler{
		backend:       b,
		verify:        true,
		maxBody:       1024,
		logger:        cap.Logger(),
		tokenVerifier: NewEd25519UploadTokenVerifier(pub),
		tokenPolicy:   "off",
	}

	data := []byte("off-mode")
	cid := storage.Compute(data)
	w := doPushWithToken(h, cid.String(), "!!!garbage!!!", data)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200 (policy off ignores token), got %d", w.Code)
	}
	cap.AssertNoWarnings(t)
}
