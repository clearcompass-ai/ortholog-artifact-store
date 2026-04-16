/*
Package api provides HTTP handlers for the artifact store.

Routes:

	POST   /v1/artifacts               → push
	GET    /v1/artifacts/{cid}         → fetch
	HEAD   /v1/artifacts/{cid}         → exists
	DELETE /v1/artifacts/{cid}         → delete
	GET    /v1/artifacts/{cid}/resolve → resolve
	POST   /v1/artifacts/{cid}/pin     → pin
	GET    /healthz                    → health

The ServerConfig.Logger is used by handlers that emit audit-trail warnings
for rejected requests (e.g., PushHandler on size-limit or digest-mismatch
rejections). If Logger is nil, handlers fall back to slog.Default(). Callers
that want their test harness to capture audit logs must pass an explicit
logger backed by a capturing slog.Handler — see internal/testutil.
*/
package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
)

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Backend       backends.BackendProvider
	VerifyOnPush  bool
	MaxBodySize   int64
	DefaultExpiry time.Duration
	Logger        *slog.Logger

	// UploadTokenVerifier, if non-nil, validates the X-Upload-Token
	// header on POST /v1/artifacts. nil disables token verification —
	// the push handler accepts every request (network segmentation
	// model).
	UploadTokenVerifier UploadTokenVerifier

	// UploadTokenPolicy controls how a missing token is handled when a
	// Verifier is configured:
	//   "off"      — tokens ignored (verifier unused)
	//   "optional" — if X-Upload-Token is present, it must verify;
	//                if absent, accept the push
	//   "required" — X-Upload-Token must be present and verify
	// The default when empty is "off".
	UploadTokenPolicy string
}

// NewMux creates the HTTP handler with all routes.
func NewMux(cfg ServerConfig) http.Handler {
	mux := http.NewServeMux()

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	policy := cfg.UploadTokenPolicy
	if policy == "" {
		policy = "off"
	}

	push := &PushHandler{
		backend:       cfg.Backend,
		verify:        cfg.VerifyOnPush,
		maxBody:       cfg.MaxBodySize,
		logger:        logger,
		tokenVerifier: cfg.UploadTokenVerifier,
		tokenPolicy:   policy,
	}
	fetch := &FetchHandler{backend: cfg.Backend}
	resolve := &ResolveHandler{backend: cfg.Backend, defaultExpiry: cfg.DefaultExpiry}
	pin := &PinHandler{backend: cfg.Backend}
	exists := &ExistsHandler{backend: cfg.Backend}
	del := &DeleteHandler{backend: cfg.Backend}
	health := &HealthHandler{backend: cfg.Backend}

	mux.Handle("POST /v1/artifacts", push)
	mux.Handle("GET /v1/artifacts/{cid}", fetch)
	mux.Handle("HEAD /v1/artifacts/{cid}", exists)
	mux.Handle("DELETE /v1/artifacts/{cid}", del)
	mux.Handle("GET /v1/artifacts/{cid}/resolve", resolve)
	mux.Handle("POST /v1/artifacts/{cid}/pin", pin)
	mux.Handle("GET /healthz", health)

	return mux
}
