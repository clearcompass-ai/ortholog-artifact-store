package api

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// PushHandler handles POST /v1/artifacts.
// Accepts a CID (X-Artifact-CID header) plus raw body bytes.
// Optionally verifies that the body hashes (under the CID's declared
// algorithm) to the CID digest. The hash function is selected by the
// CID's algorithm tag — SHA-256 today, whatever the SDK registers
// tomorrow via storage.RegisterAlgorithm. The artifact store never
// hard-codes a single hash function.
//
// Audit trail:
//
// Every rejection emits a structured slog.Warn record with a stable
// event/reason pair so log pipelines can alert on specific failure
// classes without parsing message text:
//
//	event  = "artifact.push.rejected"
//	reason ∈ {
//	  "missing_cid_header",      // no X-Artifact-CID
//	  "invalid_cid_header",      // malformed CID (algo unknown, bad hex, …)
//	  "read_body_error",         // I/O error reading body
//	  "size_exceeded",           // body > MaxBodySize
//	  "cid_mismatch",            // hash(body, cid.Algorithm) != cid.Digest
//	  "token_required_missing",  // policy requires token, none provided
//	  "token_invalid",           // token failed cryptographic verification
//	  "token_cid_mismatch",      // token binds a different CID
//	  "token_size_mismatch",     // token binds a different size
//	  "token_expired",           // token past its exp time
//	  "token_malformed",         // payload/signature couldn't be parsed
//	  "backend_error",           // backend Push returned a generic error
//	}
//
// Each record also carries: remote_addr, claimed_cid, received_size,
// max_body_size, cid_algorithm (algorithm name from the parsed CID),
// computed_cid (on cid_mismatch — the CID under cid.Algorithm that the
// received bytes actually hash to), claimed_digest, operator_token_kid
// (when a token was provided).
//
// Callers (SIEM/monitoring) should alert on reason ∈ {cid_mismatch,
// size_exceeded, token_invalid, token_expired} — under normal operation
// these should never fire because the upstream operator's quota and
// signing pipeline catches them first.
type PushHandler struct {
	backend       backends.BackendProvider
	verify        bool
	maxBody       int64
	logger        *slog.Logger
	tokenVerifier UploadTokenVerifier
	tokenPolicy   string // "off" | "optional" | "required"
}

const pushRejectedEvent = "artifact.push.rejected"

func (h *PushHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// ── 1. Parse CID header ─────────────────────────────────────
	cidStr := r.Header.Get("X-Artifact-CID")
	if cidStr == "" {
		h.rejectMalformed(w, r, "", 0, "missing_cid_header", "missing X-Artifact-CID header")
		return
	}
	cid, err := storage.ParseCID(cidStr)
	if err != nil {
		h.rejectMalformed(w, r, cidStr, 0, "invalid_cid_header",
			fmt.Sprintf("invalid CID: %v", err))
		return
	}

	// ── 2. Read body with size limit (+1 to detect overflow) ────
	body := io.LimitReader(r.Body, h.maxBody+1)
	data, err := io.ReadAll(body)
	if err != nil {
		h.rejectMalformed(w, r, cidStr, int64(len(data)), "read_body_error",
			fmt.Sprintf("read body: %v", err))
		return
	}
	receivedSize := int64(len(data))
	if receivedSize > h.maxBody {
		h.logger.Warn("push rejected: body exceeds size limit",
			"event", pushRejectedEvent,
			"reason", "size_exceeded",
			"claimed_cid", cidStr,
			"remote_addr", r.RemoteAddr,
			"received_size", receivedSize,
			"max_body_size", h.maxBody,
		)
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("body size exceeds %d bytes", h.maxBody))
		return
	}

	// ── 3. Upload token check (AS-2) ────────────────────────────
	// Must run BEFORE digest verification so a client can't exploit
	// the verification path to probe for valid CIDs.
	tokenKid := h.checkUploadToken(w, r, cidStr, receivedSize)
	if tokenKid == tokenRejected {
		return // response already written
	}

	// ── 4. Digest verification ──────────────────────────────────
	// Verification, not computation — the SDK computed the CID. The
	// hash algorithm is selected by the CID's algorithm tag (parsed
	// at step 1), so a CID minted under any algorithm registered in
	// storage.RegisterAlgorithm verifies correctly here without code
	// changes. ParseCID has already enforced "algorithm registered"
	// and "digest length matches algorithm output size", so a Verify
	// failure at this point is unambiguously a content mismatch
	// (truncation, bit flip, claim/body disagreement).
	if h.verify {
		if !cid.Verify(data) {
			computedCID := storage.ComputeWith(data, cid.Algorithm)
			h.logger.Warn("push rejected: CID digest mismatch (data corruption)",
				"event", pushRejectedEvent,
				"reason", "cid_mismatch",
				"claimed_cid", cidStr,
				"cid_algorithm", algorithmName(cid),
				"remote_addr", r.RemoteAddr,
				"received_size", receivedSize,
				"computed_cid", computedCID.String(),
				"claimed_digest", hex.EncodeToString(cid.Digest),
				"operator_token_kid", tokenKid,
			)
			writeError(w, http.StatusBadRequest,
				"body bytes do not match CID digest (data corruption)")
			return
		}
	}

	// ── 5. Persist ──────────────────────────────────────────────
	if err := h.backend.Push(cid, data); err != nil {
		h.logger.Warn("push rejected: backend error",
			"event", pushRejectedEvent,
			"reason", "backend_error",
			"claimed_cid", cidStr,
			"remote_addr", r.RemoteAddr,
			"received_size", receivedSize,
			"error", err.Error(),
		)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"cid":  cid.String(),
		"size": len(data),
	})
}

// tokenRejected is the sentinel return value from checkUploadToken
// meaning "a rejection has already been written; caller should return".
const tokenRejected = "<rejected>"

// checkUploadToken applies the configured policy. Returns:
//   - empty string ""   — no token expected (policy "off") or no token
//                          presented under "optional"; push may proceed
//                          without a token identifier in logs
//   - the token's kid   — token was present and verified
//   - tokenRejected     — a rejection has been written; caller must return
func (h *PushHandler) checkUploadToken(w http.ResponseWriter, r *http.Request, cidStr string, size int64) string {
	if h.tokenPolicy == "off" || h.tokenVerifier == nil {
		return ""
	}

	tokenStr := r.Header.Get("X-Upload-Token")
	if tokenStr == "" {
		if h.tokenPolicy == "required" {
			h.logger.Warn("push rejected: upload token required but missing",
				"event", pushRejectedEvent,
				"reason", "token_required_missing",
				"claimed_cid", cidStr,
				"remote_addr", r.RemoteAddr,
				"received_size", size,
			)
			writeError(w, http.StatusUnauthorized, "X-Upload-Token required")
			return tokenRejected
		}
		return "" // optional, missing → accept
	}

	if err := h.tokenVerifier.Verify(tokenStr, cidStr, size, time.Now()); err != nil {
		reason := classifyTokenError(err)
		h.logger.Warn("push rejected: upload token failed",
			"event", pushRejectedEvent,
			"reason", reason,
			"claimed_cid", cidStr,
			"remote_addr", r.RemoteAddr,
			"received_size", size,
			"error", err.Error(),
		)
		writeError(w, http.StatusUnauthorized, err.Error())
		return tokenRejected
	}

	// Extract kid for downstream audit attributes. The verifier doesn't
	// expose internals; we parse the payload ourselves (signature was
	// already verified by Verify, so this is trusted).
	return extractKid(tokenStr)
}

// classifyTokenError maps sentinel token errors to stable log reasons.
func classifyTokenError(err error) string {
	switch {
	case errors.Is(err, ErrTokenBadSignature):
		return "token_invalid"
	case errors.Is(err, ErrTokenExpired):
		return "token_expired"
	case errors.Is(err, ErrTokenCIDMismatch):
		return "token_cid_mismatch"
	case errors.Is(err, ErrTokenSizeMismatch):
		return "token_size_mismatch"
	case errors.Is(err, ErrTokenMalformed), errors.Is(err, ErrTokenPayloadInvalid):
		return "token_malformed"
	default:
		return "token_invalid"
	}
}

// extractKid pulls the payload's kid for logging. Returns empty string
// if the token is malformed (we just verified it, so normally fine).
func extractKid(token string) string {
	dot := strings.IndexByte(token, '.')
	if dot < 1 {
		return ""
	}
	var kid struct {
		Kid string `json:"kid"`
	}
	// Try raw-URL, then std-URL — matches the verifier's tolerance.
	for _, dec := range []func(string) ([]byte, error){
		base64.RawURLEncoding.DecodeString,
		base64.URLEncoding.DecodeString,
	} {
		if payloadBytes, err := dec(token[:dot]); err == nil {
			if err := json.Unmarshal(payloadBytes, &kid); err == nil {
				return kid.Kid
			}
		}
	}
	return ""
}

// rejectMalformed writes a 400 with a structured audit log. Used for
// the "the request itself is bad" pre-token failures.
func (h *PushHandler) rejectMalformed(w http.ResponseWriter, r *http.Request, cidStr string, size int64, reason, userMsg string) {
	h.logger.Warn("push rejected: "+reason,
		"event", pushRejectedEvent,
		"reason", reason,
		"claimed_cid", cidStr,
		"remote_addr", r.RemoteAddr,
		"received_size", size,
	)
	writeError(w, http.StatusBadRequest, userMsg)
}

// algorithmName returns the algorithm name prefix from a parsed CID's
// canonical string form (e.g. "sha256" from "sha256:abc…"). It exists
// solely to surface a discrete cid_algorithm field in the cid_mismatch
// audit record, so a SIEM can alert on, group by, or break out
// rejections per algorithm without parsing claimed_cid. The SDK does
// not export an algorithm-tag→name lookup; cid.String() does, prefixed
// at the first ':'. ParseCID has already validated the prefix at the
// step-1 header parse, so the colon is guaranteed present here.
func algorithmName(c storage.CID) string {
	s := c.String()
	if i := strings.IndexByte(s, ':'); i > 0 {
		return s[:i]
	}
	return ""
}
