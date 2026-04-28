package backends

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// ─── Test doubles ────────────────────────────────────────────────────

// scriptedBackend lets a test decide per-call which error to return.
// Wraps an InMemoryBackend so the successful path produces real state.
type scriptedBackend struct {
	inner *InMemoryBackend

	mu        sync.Mutex
	pushErr   error
	fetchErr  error
	existsErr error
	pinErr    error
	deleteErr error
	healthErr error

	// Counters for assertion.
	pushCount   int
	fetchCount  int
	existsCount int
}

func newScriptedBackend() *scriptedBackend {
	return &scriptedBackend{inner: NewInMemoryBackend()}
}

func (s *scriptedBackend) setPushErr(err error)   { s.mu.Lock(); s.pushErr = err; s.mu.Unlock() }
func (s *scriptedBackend) setFetchErr(err error)  { s.mu.Lock(); s.fetchErr = err; s.mu.Unlock() }
func (s *scriptedBackend) setExistsErr(err error) { s.mu.Lock(); s.existsErr = err; s.mu.Unlock() }
func (s *scriptedBackend) setPinErr(err error)    { s.mu.Lock(); s.pinErr = err; s.mu.Unlock() }
func (s *scriptedBackend) setDeleteErr(err error) { s.mu.Lock(); s.deleteErr = err; s.mu.Unlock() }

func (s *scriptedBackend) Push(cid storage.CID, data []byte) error {
	s.mu.Lock()
	s.pushCount++
	err := s.pushErr
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.inner.Push(cid, data)
}

func (s *scriptedBackend) Fetch(cid storage.CID) ([]byte, error) {
	s.mu.Lock()
	s.fetchCount++
	err := s.fetchErr
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return s.inner.Fetch(cid)
}

func (s *scriptedBackend) Exists(cid storage.CID) (bool, error) {
	s.mu.Lock()
	s.existsCount++
	err := s.existsErr
	s.mu.Unlock()
	if err != nil {
		return false, err
	}
	return s.inner.Exists(cid)
}

func (s *scriptedBackend) Pin(cid storage.CID) error {
	s.mu.Lock()
	err := s.pinErr
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.inner.Pin(cid)
}

func (s *scriptedBackend) Delete(cid storage.CID) error {
	s.mu.Lock()
	err := s.deleteErr
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.inner.Delete(cid)
}

func (s *scriptedBackend) Resolve(cid storage.CID, expiry time.Duration) (*storage.RetrievalCredential, error) {
	return s.inner.Resolve(cid, expiry)
}

func (s *scriptedBackend) Healthy() error {
	s.mu.Lock()
	err := s.healthErr
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.inner.Healthy()
}

// quietLogger discards log output — tests that check behavior, not logs,
// use this to avoid test runner noise.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// ─── Sync mode ───────────────────────────────────────────────────────

func TestMirrored_Sync_PushBothReceiveBytes(t *testing.T) {
	primary := newScriptedBackend()
	mirror := newScriptedBackend()
	ms := NewMirroredStore(primary, mirror, MirroredConfig{Mode: MirrorModeSync, Logger: quietLogger()})
	t.Cleanup(ms.Close)

	data := []byte("sync-double-write")
	cid := storage.Compute(data)
	if err := ms.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}

	pGot, err := primary.inner.Fetch(cid)
	if err != nil {
		t.Fatalf("primary Fetch: %v", err)
	}
	mGot, err := mirror.inner.Fetch(cid)
	if err != nil {
		t.Fatalf("mirror Fetch: %v", err)
	}
	if !bytes.Equal(pGot, data) || !bytes.Equal(mGot, data) {
		t.Fatal("sync mode: both backends must hold identical bytes")
	}
}

func TestMirrored_Sync_PrimaryFailReturnsError(t *testing.T) {
	primary := newScriptedBackend()
	primary.setPushErr(errors.New("primary down"))
	mirror := newScriptedBackend()
	ms := NewMirroredStore(primary, mirror, MirroredConfig{Mode: MirrorModeSync, Logger: quietLogger()})
	t.Cleanup(ms.Close)

	data := []byte("x")
	err := ms.Push(storage.Compute(data), data)
	if err == nil {
		t.Fatal("primary fail must propagate as error")
	}
	// Mirror should NOT have been written to if primary failed.
	if mirror.pushCount != 0 {
		t.Fatalf("mirror Push count: want 0 when primary fails, got %d", mirror.pushCount)
	}
}

func TestMirrored_Sync_MirrorFailIsNonFatal(t *testing.T) {
	primary := newScriptedBackend()
	mirror := newScriptedBackend()
	mirror.setPushErr(errors.New("mirror flaky"))
	ms := NewMirroredStore(primary, mirror, MirroredConfig{Mode: MirrorModeSync, Logger: quietLogger()})
	t.Cleanup(ms.Close)

	data := []byte("mirror-flake")
	cid := storage.Compute(data)
	if err := ms.Push(cid, data); err != nil {
		t.Fatalf("mirror fail must be non-fatal: %v", err)
	}
	// Primary must have received the data.
	got, err := primary.inner.Fetch(cid)
	if err != nil {
		t.Fatalf("primary Fetch: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("primary must hold the bytes even when mirror fails")
	}
}

func TestMirrored_Sync_FetchFallsBackToMirror(t *testing.T) {
	primary := newScriptedBackend()
	mirror := newScriptedBackend()
	ms := NewMirroredStore(primary, mirror, MirroredConfig{Mode: MirrorModeSync, Logger: quietLogger()})
	t.Cleanup(ms.Close)

	// Push only to mirror (bypass primary entirely) then force primary Fetch to error.
	data := []byte("fallback")
	cid := storage.Compute(data)
	if err := mirror.inner.Push(cid, data); err != nil {
		t.Fatal(err)
	}
	primary.setFetchErr(errors.New("primary is sad"))

	got, err := ms.Fetch(cid)
	if err != nil {
		t.Fatalf("Fetch should have fallen back: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("fallback Fetch returned wrong bytes")
	}
	if primary.fetchCount == 0 {
		t.Fatal("primary Fetch was never attempted — fallback logic is wrong")
	}
	if mirror.fetchCount == 0 {
		t.Fatal("mirror Fetch was never called — fallback did not happen")
	}
}

func TestMirrored_Sync_ExistsFallsBackToMirror(t *testing.T) {
	primary := newScriptedBackend()
	mirror := newScriptedBackend()
	ms := NewMirroredStore(primary, mirror, MirroredConfig{Mode: MirrorModeSync, Logger: quietLogger()})
	t.Cleanup(ms.Close)

	data := []byte("exists-fallback")
	cid := storage.Compute(data)
	// Only the mirror has the object.
	if err := mirror.inner.Push(cid, data); err != nil {
		t.Fatal(err)
	}

	exists, err := ms.Exists(cid)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatal("Exists should return true via mirror fallback")
	}
}

func TestMirrored_Sync_DeleteHitsBoth(t *testing.T) {
	primary := newScriptedBackend()
	mirror := newScriptedBackend()
	ms := NewMirroredStore(primary, mirror, MirroredConfig{Mode: MirrorModeSync, Logger: quietLogger()})
	t.Cleanup(ms.Close)

	data := []byte("del-both")
	cid := storage.Compute(data)
	if err := ms.Push(cid, data); err != nil {
		t.Fatal(err)
	}
	if err := ms.Delete(cid); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if e, _ := primary.inner.Exists(cid); e {
		t.Fatal("primary still has deleted object")
	}
	if e, _ := mirror.inner.Exists(cid); e {
		t.Fatal("mirror still has deleted object")
	}
}

func TestMirrored_Sync_ResolveUsesPrimary(t *testing.T) {
	primary := newScriptedBackend()
	mirror := newScriptedBackend()
	ms := NewMirroredStore(primary, mirror, MirroredConfig{Mode: MirrorModeSync, Logger: quietLogger()})
	t.Cleanup(ms.Close)

	data := []byte("resolve")
	cid := storage.Compute(data)
	if err := ms.Push(cid, data); err != nil {
		t.Fatal(err)
	}

	cred, err := ms.Resolve(cid, time.Hour)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cred.Method != storage.MethodDirect {
		t.Fatalf("Resolve must use primary (InMemoryBackend=MethodDirect), got %s", cred.Method)
	}
}

// ─── Healthy propagation ─────────────────────────────────────────────

func TestMirrored_Healthy_BothOK(t *testing.T) {
	ms := NewMirroredStore(newScriptedBackend(), newScriptedBackend(),
		MirroredConfig{Mode: MirrorModeSync, Logger: quietLogger()})
	t.Cleanup(ms.Close)
	if err := ms.Healthy(); err != nil {
		t.Fatalf("Healthy: %v", err)
	}
}

func TestMirrored_Healthy_PrimaryFailReturnsPrimaryError(t *testing.T) {
	primary := newScriptedBackend()
	primary.healthErr = errors.New("primary unhealthy")
	mirror := newScriptedBackend()
	ms := NewMirroredStore(primary, mirror, MirroredConfig{Mode: MirrorModeSync, Logger: quietLogger()})
	t.Cleanup(ms.Close)
	err := ms.Healthy()
	if err == nil || err.Error() != "primary unhealthy" {
		t.Fatalf("Healthy: want 'primary unhealthy', got %v", err)
	}
}

func TestMirrored_Healthy_MirrorFailReturnsMirrorError(t *testing.T) {
	primary := newScriptedBackend()
	mirror := newScriptedBackend()
	mirror.healthErr = errors.New("mirror unhealthy")
	ms := NewMirroredStore(primary, mirror, MirroredConfig{Mode: MirrorModeSync, Logger: quietLogger()})
	t.Cleanup(ms.Close)
	err := ms.Healthy()
	if err == nil || err.Error() != "mirror unhealthy" {
		t.Fatalf("Healthy: want 'mirror unhealthy', got %v", err)
	}
}

// ─── Concurrency (-race surface) ─────────────────────────────────────

func TestMirrored_Sync_ConcurrentPush(t *testing.T) {
	primary := newScriptedBackend()
	mirror := newScriptedBackend()
	ms := NewMirroredStore(primary, mirror, MirroredConfig{Mode: MirrorModeSync, Logger: quietLogger()})
	t.Cleanup(ms.Close)

	const N = 100
	var wg sync.WaitGroup
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			data := []byte(fmt.Sprintf("concurrent-%d", i))
			errs[i] = ms.Push(storage.Compute(data), data)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Push[%d]: %v", i, err)
		}
	}
	if primary.pushCount != N {
		t.Fatalf("primary pushCount: want %d, got %d", N, primary.pushCount)
	}
	if mirror.pushCount != N {
		t.Fatalf("mirror pushCount: want %d, got %d", N, mirror.pushCount)
	}
}


// ─── Sync mode is the only supported mode ────────────────────────────

func TestMirrored_DefaultModeIsSync(t *testing.T) {
	// MirroredConfig{} (zero-value Mode) must produce a working sync
	// mirror. The pre-v7.75 async_pin mode targeted IPFS-IPFS
	// replication and is gone now that IPFS is no longer a supported
	// backend kind; sync is the only mode.
	ms := NewMirroredStore(newScriptedBackend(), newScriptedBackend(), MirroredConfig{})
	t.Cleanup(ms.Close)

	data := []byte("default-mode")
	cid := storage.Compute(data)
	if err := ms.Push(cid, data); err != nil {
		t.Fatalf("Push under default (sync) mode: %v", err)
	}
}

func TestMirrored_UnknownModeFallsBackToSync(t *testing.T) {
	// An unknown Mode string must NOT crash and must not silently
	// degrade to a different semantics — it falls back to sync with a
	// warning so misconfiguration is visible without breaking the
	// service.
	primary := newScriptedBackend()
	mirror := newScriptedBackend()
	ms := NewMirroredStore(primary, mirror, MirroredConfig{
		Mode:   "this-mode-does-not-exist",
		Logger: quietLogger(),
	})
	t.Cleanup(ms.Close)

	data := []byte("unknown-mode")
	cid := storage.Compute(data)
	if err := ms.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if primary.pushCount != 1 || mirror.pushCount != 1 {
		t.Fatalf("unknown mode should still write both: primary=%d mirror=%d",
			primary.pushCount, mirror.pushCount)
	}
}

func TestMirrored_NilLoggerFallback(t *testing.T) {
	// NilLogger in the config → slog.Default(). No panic on warning path.
	primary := newScriptedBackend()
	mirror := newScriptedBackend()
	mirror.setPushErr(errors.New("mirror down")) // triggers warning path
	ms := NewMirroredStore(primary, mirror, MirroredConfig{Logger: nil})
	t.Cleanup(ms.Close)

	data := []byte("nil-logger")
	cid := storage.Compute(data)
	// Must not panic even though the mirror failure triggers a Warn log.
	if err := ms.Push(cid, data); err != nil {
		t.Fatalf("Push with nil logger: %v", err)
	}
}

// ─── Close (no-op safety) ────────────────────────────────────────────

// TestMirrored_Close_IsNoOp guards the Close contract: it must be safe
// to call zero, one, or many times. The pre-v7.75 async-pin worker
// owned a goroutine that Close shut down; the current sync-only mirror
// has no background work and Close is a no-op for source compatibility.
func TestMirrored_Close_IsNoOp(t *testing.T) {
	ms := NewMirroredStore(newScriptedBackend(), newScriptedBackend(), MirroredConfig{Logger: quietLogger()})
	ms.Close()
	ms.Close() // double-Close must not panic
}

// ─── Pin (both backends, fail-tolerant on mirror) ────────────────────

func TestMirrored_Pin_BothBackendsCalled(t *testing.T) {
	primary := newScriptedBackend()
	mirror := newScriptedBackend()
	ms := NewMirroredStore(primary, mirror, MirroredConfig{Logger: quietLogger()})
	t.Cleanup(ms.Close)

	data := []byte("pin-both")
	cid := storage.Compute(data)
	// Pre-populate so InMemoryBackend.Pin succeeds.
	if err := primary.inner.Push(cid, data); err != nil {
		t.Fatalf("primary inner Push: %v", err)
	}
	if err := mirror.inner.Push(cid, data); err != nil {
		t.Fatalf("mirror inner Push: %v", err)
	}

	if err := ms.Pin(cid); err != nil {
		t.Fatalf("Pin: %v", err)
	}
}

func TestMirrored_Pin_MirrorFailure_NonFatal(t *testing.T) {
	primary := newScriptedBackend()
	mirror := newScriptedBackend()
	mirror.setPinErr(errors.New("mirror pin down"))
	ms := NewMirroredStore(primary, mirror, MirroredConfig{Logger: quietLogger()})
	t.Cleanup(ms.Close)

	data := []byte("pin-mirror-fail")
	cid := storage.Compute(data)
	if err := primary.inner.Push(cid, data); err != nil {
		t.Fatal(err)
	}

	// The error returned is the primary's; mirror failure is a Warn.
	if err := ms.Pin(cid); err != nil {
		t.Fatalf("Pin: want primary nil despite mirror failure, got %v", err)
	}
}

func TestMirrored_Pin_PrimaryFailure_Surfaced(t *testing.T) {
	primary := newScriptedBackend()
	mirror := newScriptedBackend()
	primary.setPinErr(errors.New("primary pin failed"))
	ms := NewMirroredStore(primary, mirror, MirroredConfig{Logger: quietLogger()})
	t.Cleanup(ms.Close)

	cid := storage.Compute([]byte("x"))
	err := ms.Pin(cid)
	if err == nil {
		t.Fatal("Pin: want primary error surfaced, got nil")
	}
	if !errors.Is(err, primary.pinErr) && err.Error() != "primary pin failed" {
		t.Fatalf("primary error not surfaced: %v", err)
	}
}

// ─── Fetch / Exists happy paths (primary serves) ────────────────────

// TestMirrored_Fetch_PrimaryServesWithoutMirror locks the happy path:
// primary returns successfully, the mirror is never consulted. The
// counter-part fall-through test below pins the failure-path.
func TestMirrored_Fetch_PrimaryServesWithoutMirror(t *testing.T) {
	primary := newScriptedBackend()
	mirror := newScriptedBackend()
	ms := NewMirroredStore(primary, mirror, MirroredConfig{Logger: quietLogger()})
	t.Cleanup(ms.Close)

	data := []byte("primary-only")
	cid := storage.Compute(data)
	// Pre-populate primary directly so push counters don't conflate.
	if err := primary.inner.Push(cid, data); err != nil {
		t.Fatal(err)
	}

	got, err := ms.Fetch(cid)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("byte mismatch on primary fetch")
	}
	if mirror.fetchCount != 0 {
		t.Fatalf("mirror Fetch must not be called when primary succeeds; got %d calls",
			mirror.fetchCount)
	}
}

// TestMirrored_Exists_PrimarySaysYes_NoMirrorCheck is the symmetric
// happy path for Exists.
func TestMirrored_Exists_PrimarySaysYes_NoMirrorCheck(t *testing.T) {
	primary := newScriptedBackend()
	mirror := newScriptedBackend()
	ms := NewMirroredStore(primary, mirror, MirroredConfig{Logger: quietLogger()})
	t.Cleanup(ms.Close)

	data := []byte("exists-primary")
	cid := storage.Compute(data)
	if err := primary.inner.Push(cid, data); err != nil {
		t.Fatal(err)
	}

	exists, err := ms.Exists(cid)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatal("Exists: want true, got false")
	}
	if mirror.existsCount != 0 {
		t.Fatalf("mirror Exists must not be called when primary says yes; got %d calls",
			mirror.existsCount)
	}
}

// ─── Fetch fall-through to mirror ────────────────────────────────────

func TestMirrored_Fetch_PrimaryFails_MirrorServes(t *testing.T) {
	primary := newScriptedBackend()
	mirror := newScriptedBackend()
	ms := NewMirroredStore(primary, mirror, MirroredConfig{Logger: quietLogger()})
	t.Cleanup(ms.Close)

	data := []byte("fall-through")
	cid := storage.Compute(data)
	// Mirror has the bytes; primary doesn't.
	if err := mirror.inner.Push(cid, data); err != nil {
		t.Fatal(err)
	}
	primary.setFetchErr(errors.New("primary down"))

	got, err := ms.Fetch(cid)
	if err != nil {
		t.Fatalf("Fetch: want mirror to serve, got %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("byte mismatch: mirror should have served the original payload")
	}
}

// ─── Exists fall-through to mirror ───────────────────────────────────

func TestMirrored_Exists_PrimaryFails_MirrorAnswers(t *testing.T) {
	primary := newScriptedBackend()
	mirror := newScriptedBackend()
	ms := NewMirroredStore(primary, mirror, MirroredConfig{Logger: quietLogger()})
	t.Cleanup(ms.Close)

	data := []byte("exists-fallthrough")
	cid := storage.Compute(data)
	if err := mirror.inner.Push(cid, data); err != nil {
		t.Fatal(err)
	}
	primary.setExistsErr(errors.New("primary err"))

	exists, err := ms.Exists(cid)
	if err != nil {
		t.Fatalf("Exists: want mirror to answer, got %v", err)
	}
	if !exists {
		t.Fatal("Exists: want true (mirror has it), got false")
	}
}

func TestMirrored_Exists_PrimaryReturnsFalse_FallsThroughToMirror(t *testing.T) {
	// Even when primary returns (false, nil) — i.e. cleanly says "not
	// here" — Exists falls through to the mirror, because the artifact
	// may have landed there during a transient.
	primary := newScriptedBackend()
	mirror := newScriptedBackend()
	ms := NewMirroredStore(primary, mirror, MirroredConfig{Logger: quietLogger()})
	t.Cleanup(ms.Close)

	data := []byte("only-on-mirror")
	cid := storage.Compute(data)
	if err := mirror.inner.Push(cid, data); err != nil {
		t.Fatal(err)
	}

	exists, err := ms.Exists(cid)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatal("Exists: want true via mirror, got false")
	}
}

// ─── Delete (mirror failure is non-fatal, surfaces primary error) ───

func TestMirrored_Delete_MirrorFailure_NonFatal(t *testing.T) {
	primary := newScriptedBackend()
	mirror := newScriptedBackend()
	mirror.setDeleteErr(errors.New("mirror delete failed"))
	ms := NewMirroredStore(primary, mirror, MirroredConfig{Logger: quietLogger()})
	t.Cleanup(ms.Close)

	cid := storage.Compute([]byte("x"))
	if err := ms.Delete(cid); err != nil {
		t.Fatalf("Delete: want primary nil despite mirror failure, got %v", err)
	}
}

func TestMirrored_Delete_PrimaryFailure_Surfaced(t *testing.T) {
	primary := newScriptedBackend()
	mirror := newScriptedBackend()
	primary.setDeleteErr(errors.New("primary delete failed"))
	ms := NewMirroredStore(primary, mirror, MirroredConfig{Logger: quietLogger()})
	t.Cleanup(ms.Close)

	cid := storage.Compute([]byte("x"))
	err := ms.Delete(cid)
	if err == nil || err.Error() != "primary delete failed" {
		t.Fatalf("Delete: want primary error surfaced, got %v", err)
	}
}
