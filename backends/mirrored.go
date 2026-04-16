/*
backends/mirrored.go — MirroredStore decorator.

Wraps two BackendProviders. Two modes:

  sync (default):
    Push writes primary, then mirror. Primary fail = error.
    Mirror fail = log warning (non-fatal). Both writes must
    attempt before returning.

  async_pin (IPFS↔IPFS only):
    Push writes primary, returns success immediately.
    Background goroutine pins on mirror. Mirror is eventually
    consistent. Bounded channel prevents unbounded goroutine growth.
*/
package backends

import (
	"log/slog"
	"time"

	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// MirrorMode selects sync or async pin behavior.
const (
	MirrorModeSync     = "sync"
	MirrorModeAsyncPin = "async_pin"
)

// MirroredConfig holds mirror settings.
type MirroredConfig struct {
	Mode   string
	Logger *slog.Logger
}

// MirroredStore decorates two BackendProviders with double-write.
type MirroredStore struct {
	primary BackendProvider
	mirror  BackendProvider
	mode    string
	logger  *slog.Logger
	pinCh   chan pinRequest
}

type pinRequest struct {
	cid  storage.CID
	data []byte
}

// NewMirroredStore creates a mirrored backend.
func NewMirroredStore(primary, mirror BackendProvider, cfg MirroredConfig) *MirroredStore {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	mode := cfg.Mode
	if mode == "" {
		mode = MirrorModeSync
	}

	ms := &MirroredStore{
		primary: primary,
		mirror:  mirror,
		mode:    mode,
		logger:  logger,
	}

	if mode == MirrorModeAsyncPin {
		ms.pinCh = make(chan pinRequest, 1024)
		go ms.asyncPinWorker()
	}

	return ms
}

func (m *MirroredStore) asyncPinWorker() {
	for req := range m.pinCh {
		if err := m.mirror.Push(req.cid, req.data); err != nil {
			m.logger.Warn("async mirror pin failed",
				"cid", req.cid.String(), "error", err)
		}
	}
}

// Close stops the async pin worker. Safe to call in sync mode (no-op).
func (m *MirroredStore) Close() {
	if m.pinCh != nil {
		close(m.pinCh)
	}
}

func (m *MirroredStore) Push(cid storage.CID, data []byte) error {
	if err := m.primary.Push(cid, data); err != nil {
		return err
	}

	switch m.mode {
	case MirrorModeAsyncPin:
		select {
		case m.pinCh <- pinRequest{cid: cid, data: data}:
		default:
			m.logger.Warn("async pin channel full, skipping mirror",
				"cid", cid.String())
		}
	default:
		if err := m.mirror.Push(cid, data); err != nil {
			m.logger.Warn("mirror push failed (non-fatal)",
				"cid", cid.String(), "error", err)
		}
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
