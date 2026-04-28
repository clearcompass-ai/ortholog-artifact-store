/*
backends/mirrored.go — MirroredStore decorator.

Wraps two ObjectStores in synchronous double-write mode:

  Push writes primary, then mirror. Primary fail = error.
  Mirror fail = log warning (non-fatal). Both writes must
  attempt before returning.

Fetch tries primary first; falls back to mirror on error so the store
keeps serving reads if one side is briefly unavailable. Exists, Pin,
Delete, Resolve, and Healthy are propagated to one or both backends as
appropriate (Resolve hits primary only — multiple signed URLs for the
same artifact would just confuse downstream routing).

The decorator is orthogonal to the backend kind: any pair of object
stores satisfying BackendProvider composes — GCS+RustFS, RustFS+RustFS
(geographically split), GCS+GCS (cross-region), etc.
*/
package backends

import (
	"log/slog"
	"time"

	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// MirrorMode is reserved for future expansion. The only supported
// value today is "sync" — synchronous double-write. Pre-v7.75 drafts
// also exposed an "async_pin" mode targeted at IPFS-IPFS replication;
// IPFS is no longer a supported backend kind so the mode is gone.
const MirrorModeSync = "sync"

// MirroredConfig holds mirror settings.
type MirroredConfig struct {
	Mode   string
	Logger *slog.Logger
}

// MirroredStore decorates two object-store BackendProviders with
// synchronous double-write.
type MirroredStore struct {
	primary BackendProvider
	mirror  BackendProvider
	logger  *slog.Logger
}

// NewMirroredStore creates a mirrored backend. Both arguments must
// satisfy BackendProvider — typically two of {GCS, RustFS} or two
// instances of the same kind in different regions.
func NewMirroredStore(primary, mirror BackendProvider, cfg MirroredConfig) *MirroredStore {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Mode != "" && cfg.Mode != MirrorModeSync {
		logger.Warn("mirror: unknown mode, defaulting to sync",
			"mode", cfg.Mode)
	}
	return &MirroredStore{
		primary: primary,
		mirror:  mirror,
		logger:  logger,
	}
}

// Close is retained as a no-op for source compatibility with the
// pre-v7.75 async-pin shape that started a goroutine in NewMirroredStore.
// The current sync-only mirror has no background work to clean up.
func (m *MirroredStore) Close() {}

func (m *MirroredStore) Push(cid storage.CID, data []byte) error {
	if err := m.primary.Push(cid, data); err != nil {
		return err
	}
	if err := m.mirror.Push(cid, data); err != nil {
		m.logger.Warn("mirror push failed (non-fatal)",
			"cid", cid.String(), "error", err)
	}
	return nil
}

func (m *MirroredStore) Fetch(cid storage.CID) ([]byte, error) {
	data, err := m.primary.Fetch(cid)
	if err == nil {
		return data, nil
	}
	m.logger.Info("primary fetch failed, trying mirror",
		"cid", cid.String(), "primary_error", err)
	return m.mirror.Fetch(cid)
}

func (m *MirroredStore) Exists(cid storage.CID) (bool, error) {
	exists, err := m.primary.Exists(cid)
	if err == nil && exists {
		return true, nil
	}
	return m.mirror.Exists(cid)
}

func (m *MirroredStore) Pin(cid storage.CID) error {
	err := m.primary.Pin(cid)
	if mirrorErr := m.mirror.Pin(cid); mirrorErr != nil {
		m.logger.Warn("mirror pin failed (non-fatal)",
			"cid", cid.String(), "error", mirrorErr)
	}
	return err
}

func (m *MirroredStore) Delete(cid storage.CID) error {
	err := m.primary.Delete(cid)
	if mirrorErr := m.mirror.Delete(cid); mirrorErr != nil {
		m.logger.Warn("mirror delete failed (non-fatal)",
			"cid", cid.String(), "error", mirrorErr)
	}
	return err
}

func (m *MirroredStore) Resolve(cid storage.CID, expiry time.Duration) (*storage.RetrievalCredential, error) {
	return m.primary.Resolve(cid, expiry)
}

func (m *MirroredStore) Healthy() error {
	if err := m.primary.Healthy(); err != nil {
		return err
	}
	return m.mirror.Healthy()
}
