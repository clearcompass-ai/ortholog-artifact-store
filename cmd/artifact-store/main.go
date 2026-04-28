/*
Artifact Store entry point.

Loads configuration, initializes backend, starts the HTTP server.

Authentication: optional X-Upload-Token header (see api/token.go). Controlled
by ARTIFACT_REQUIRE_UPLOAD_TOKEN:
  - "off"      (default): no token check; rely on network segmentation.
  - "optional": if the header is present, it must verify; missing is OK.
  - "required": the header must be present and verify on every push.

Startup semantics:
  - A structured warning is emitted at startup when VerifyOnPush is disabled.
  - When ORTHOLOG_ENV=production AND VerifyOnPush=false, a background
    goroutine re-emits the warning every 60 seconds. The misconfiguration
    remains visible across the full lifetime of a long-running process,
    not just at boot.
  - The logger is passed into api.NewMux so handlers can emit audit
    warnings on push rejections (see api/push.go).
*/
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/clearcompass-ai/ortholog-artifact-store/api"
	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-artifact-store/config"
)

// verifyWarnInterval controls how often the production re-warning fires.
// A package-level var (not a const) so tests can override it to a short
// value without waiting 60 seconds for a tick.
var verifyWarnInterval = 60 * time.Second

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load()
	if err != nil {
		logger.Error("config load failed", "error", err)
		os.Exit(1)
	}

	// Startup warning + optional periodic re-warning in production.
	// See runVerifyOnPushWatchdog for the full policy.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runVerifyOnPushWatchdog(ctx, cfg, logger)

	// Build optional upload-token verifier. See api/token.go. Returns nil
	// when policy is "off" (the default) — the store runs unauthenticated
	// and relies on network segmentation.
	verifier, err := buildUploadTokenVerifier(cfg, logger)
	if err != nil {
		logger.Error("upload token verifier init failed", "error", err)
		os.Exit(1)
	}

	backend, err := initBackend(cfg, logger)
	if err != nil {
		logger.Error("backend init failed", "error", err)
		os.Exit(1)
	}

	mux := api.NewMux(api.ServerConfig{
		Backend:             backend,
		VerifyOnPush:        cfg.VerifyOnPush,
		MaxBodySize:         cfg.MaxBodySize,
		DefaultExpiry:       cfg.DefaultResolveExpiry,
		Logger:              logger,
		UploadTokenVerifier: verifier,
		UploadTokenPolicy:   cfg.RequireUploadToken,
	})

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		logger.Info("shutting down", "signal", sig)
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutCancel()
		_ = srv.Shutdown(shutCtx)
		cancel() // stop the watchdog too
	}()

	logger.Info("artifact store starting",
		"addr", cfg.ListenAddr,
		"backend", cfg.Backend,
		"env", cfg.Env,
		"verify_on_push", cfg.VerifyOnPush,
		"upload_token_policy", cfg.RequireUploadToken,
		"max_body_size", cfg.MaxBodySize,
	)

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
	logger.Info("artifact store stopped")
}

// runVerifyOnPushWatchdog emits the VerifyOnPush=false warning and, when
// ORTHOLOG_ENV=production, re-emits it on a ticker until ctx is cancelled.
//
// Policy:
//   - VerifyOnPush=true: no warning, no goroutine.
//   - VerifyOnPush=false, env != production: one startup warning, no ticker.
//     (Disabling verification in dev/test/staging is often legitimate —
//     e.g., a test harness exercising the "corrupt bytes" path. Re-warning
//     every minute would just train operators to ignore the signal.)
//   - VerifyOnPush=false, env == production: startup warning AND a ticker
//     goroutine that re-emits every verifyWarnInterval. Operators reading
//     logs at any point during the process lifetime will see the warning.
//
// The goroutine is bounded to ctx's lifetime. Graceful shutdown cancels
// ctx, the ticker stops, the goroutine exits. goleak validates no leak.
func runVerifyOnPushWatchdog(ctx context.Context, cfg *config.Config, logger *slog.Logger) {
	if cfg.VerifyOnPush {
		return // happy path — nothing to warn about
	}

	const msg = "VerifyOnPush is disabled. This is a severe misconfiguration in production environments."
	attrs := []any{
		"event", "artifact.config.verify_on_push_disabled",
		"env", cfg.Env,
	}

	// Always fire at least once so it lands in startup logs.
	logger.Warn(msg, attrs...)

	if cfg.Env != "production" {
		return // dev/staging: one-shot warning, no ticker
	}

	// Production path: keep it noisy.
	go func() {
		ticker := time.NewTicker(verifyWarnInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				logger.Warn(msg, attrs...)
			}
		}
	}()
}

// initBackend wires the primary backend, optionally wrapped in a
// MirroredStore if ARTIFACT_MIRROR_BACKEND is set.
func initBackend(cfg *config.Config, logger *slog.Logger) (backends.BackendProvider, error) {
	primary, err := createBackend(cfg.Backend, cfg, false, logger)
	if err != nil {
		return nil, fmt.Errorf("primary backend: %w", err)
	}

	if cfg.MirrorBackend != "" {
		mirror, err := createBackend(cfg.MirrorBackend, cfg, true, logger)
		if err != nil {
			return nil, fmt.Errorf("mirror backend: %w", err)
		}
		return backends.NewMirroredStore(primary, mirror, backends.MirroredConfig{
			Mode:   cfg.MirrorMode,
			Logger: logger,
		}), nil
	}

	return primary, nil
}

// createBackend resolves a backend name to a constructed BackendProvider.
// The isMirror flag selects mirror-specific config fields.
func createBackend(name string, cfg *config.Config, isMirror bool, _ *slog.Logger) (backends.BackendProvider, error) {
	switch name {
	case "memory":
		return backends.NewInMemoryBackend(), nil

	case "gcs":
		bucket := cfg.Bucket
		if isMirror {
			bucket = cfg.MirrorBucket
		}
		return backends.NewGCSBackend(backends.GCSConfig{
			Bucket: bucket,
			Prefix: cfg.Prefix,
		}), nil

	case "rustfs":
		endpoint := cfg.Endpoint
		bucket := cfg.Bucket
		if isMirror {
			endpoint = cfg.MirrorEndpoint
			bucket = cfg.MirrorBucket
		}
		return backends.NewRustFSBackend(backends.RustFSConfig{
			Endpoint:  endpoint,
			Bucket:    bucket,
			Region:    cfg.Region,
			Prefix:    cfg.Prefix,
			PathStyle: cfg.PathStyle,
		}), nil

	case "ipfs":
		endpoint := cfg.Endpoint
		token := cfg.IPFSBearerToken
		if isMirror {
			endpoint = cfg.MirrorEndpoint
			token = cfg.MirrorBearerToken
		}
		if endpoint == "" {
			endpoint = "http://localhost:5001"
		}
		return backends.NewIPFSBackend(backends.IPFSConfig{
			APIEndpoint: endpoint,
			Gateway:     cfg.IPFSGateway,
			BearerToken: token,
		}), nil

	default:
		return nil, fmt.Errorf("unknown backend %q", name)
	}
}
