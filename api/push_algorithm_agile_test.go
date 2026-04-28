package api

import (
	"crypto/sha256"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// ─── v7.75 algorithm-agility regression suite ────────────────────────
//
// SDK v7.75 (ADR-005 §2) makes storage.RegisterAlgorithm a public part
// of the CID contract: any algorithm registered there must round-trip
// through every consumer of CID, including the artifact store. Push's
// digest verification (api/push.go) used to hard-code SHA-256, which
// silently rejected valid pushes whose CID was minted under any other
// registered algorithm. This file pins the algorithm-agile contract:
//
//   - Push under a non-default registered algorithm succeeds with
//     verify=true.
//   - Round-trip Fetch returns the exact bytes pushed.
//   - cid_mismatch audit records carry cid_algorithm equal to the
//     algorithm name from the parsed CID, so a SIEM dashboard can
//     break out rejections per algorithm.
//
// The test registers a synthetic algorithm with a non-32-byte digest
// (truncated SHA-256, 16 bytes) so a regression that re-introduces a
// `len(cid.Digest) != 32` guard would fail loudly. Using a different
// algorithm tag and digest size from any SDK-internal test fixture
// avoids cross-test fixture collision.

const (
	// agileTestAlgoTag is the multicodec-style 1-byte tag used for the
	// synthetic algorithm registered by these tests. 0xC1 is outside
	// the SDK's pinned AlgoSHA256 (0x12) and outside the reserved tags
	// used by the SDK's own cid_test.go fixtures (0xE2, 0xF1).
	agileTestAlgoTag storage.HashAlgorithm = 0xC1

	// agileTestAlgoName is the canonical string prefix for CIDs minted
	// under agileTestAlgoTag. Format: "<name>:<hex>".
	agileTestAlgoName = "test-algo-trunc16"

	// agileTestDigestSize forces a non-default digest length so any
	// regression that re-pins a 32-byte assumption fails immediately.
	agileTestDigestSize = 16
)

// registerAgileTestAlgorithmOnce ensures the synthetic algorithm is
// registered exactly once across the test binary. RegisterAlgorithm
// is idempotent (it overwrites map entries), but doing it once keeps
// the audit trail of where the registration happened obvious.
var registerAgileTestAlgorithmOnce sync.Once

func registerAgileTestAlgorithm(t *testing.T) {
	t.Helper()
	registerAgileTestAlgorithmOnce.Do(func() {
		storage.RegisterAlgorithm(
			agileTestAlgoTag,
			agileTestAlgoName,
			agileTestDigestSize,
			func(data []byte) []byte {
				h := sha256.Sum256(data)
				return h[:agileTestDigestSize]
			},
		)
	})
}

func TestPush_AgileAlgorithm_HappyPath(t *testing.T) {
	registerAgileTestAlgorithm(t)

	data := []byte("payload-under-non-default-algorithm")
	cid := storage.ComputeWith(data, agileTestAlgoTag)

	// Sanity: the CID we built actually uses the synthetic algorithm.
	if got := cid.String(); !strings.HasPrefix(got, agileTestAlgoName+":") {
		t.Fatalf("test setup: cid.String()=%q, want prefix %q", got, agileTestAlgoName+":")
	}
	if len(cid.Digest) != agileTestDigestSize {
		t.Fatalf("test setup: digest size=%d, want %d", len(cid.Digest), agileTestDigestSize)
	}

	h, b, cap := newTestPush(t, true /* verify */, 1024)

	w := doPush(h, cid.String(), data)
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200 (verify=true under registered non-default algorithm), got %d; body=%s",
			w.Code, w.Body.String())
	}
	cap.AssertNoWarnings(t)

	got, err := b.Fetch(cid)
	if err != nil {
		t.Fatalf("Fetch under %s: %v", agileTestAlgoName, err)
	}
	if string(got) != string(data) {
		t.Fatalf("round-trip mismatch under %s:\n  want=%q\n  got =%q", agileTestAlgoName, data, got)
	}
}

func TestPush_AgileAlgorithm_DigestMismatch_CarriesAlgorithmInAudit(t *testing.T) {
	registerAgileTestAlgorithm(t)

	// Mint a CID for one payload, send different bytes — both under the
	// synthetic algorithm. The handler must reject AND tag the audit
	// record with cid_algorithm = synthetic algorithm name, not "sha256".
	claimedCID := storage.ComputeWith([]byte("the-real-payload"), agileTestAlgoTag)
	corruptedBody := []byte("definitely-not-the-real-payload")

	h, b, cap := newTestPush(t, true /* verify */, 1024)

	w := doPush(h, claimedCID.String(), corruptedBody)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400 (cid_mismatch under synthetic algorithm), got %d; body=%s",
			w.Code, w.Body.String())
	}

	computedCID := storage.ComputeWith(corruptedBody, agileTestAlgoTag)
	cap.AssertContains(t, slog.LevelWarn, "push rejected: CID digest mismatch",
		map[string]any{
			"event":         "artifact.push.rejected",
			"reason":        "cid_mismatch",
			"claimed_cid":   claimedCID.String(),
			"cid_algorithm": agileTestAlgoName,
			"remote_addr":   "192.0.2.1:12345",
			"received_size": int64(len(corruptedBody)),
			"computed_cid":  computedCID.String(),
		})

	exists, err := b.Exists(claimedCID)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Fatal("mismatched body was stored despite rejection under synthetic algorithm")
	}
}

func TestPush_AgileAlgorithm_VerifyOff_AcceptsAnyBytes(t *testing.T) {
	// With verify=false, the handler stores whatever it receives — the
	// algorithm-agile path must inherit this property: a non-default
	// algorithm CID + arbitrary body succeeds when verify=false. This
	// pins symmetry with the existing TestPush_HappyPath_VerifyOff_Mismatch
	// for the SHA-256 path.
	registerAgileTestAlgorithm(t)

	claimedCID := storage.ComputeWith([]byte("nominal"), agileTestAlgoTag)
	body := []byte("not-nominal-but-verify-is-off")

	h, b, cap := newTestPush(t, false /* verify */, 1024)
	w := doPush(h, claimedCID.String(), body)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200 (verify=false), got %d; body=%s", w.Code, w.Body.String())
	}
	cap.AssertNoWarnings(t)

	got, err := b.Fetch(claimedCID)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("verify=false should store the bytes verbatim under the claimed CID")
	}
}
