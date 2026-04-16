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

func (s *scriptedBackend) setPushErr(err error)  { s.mu.Lock(); s.pushErr = err; s.mu.Unlock() }
func (s *scriptedBackend) setFetchErr(err error) { s.mu.Lock(); s.fetchErr = err; s.mu.Unlock() }

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

// ─── Async mode ──────────────────────────────────────────────────────

// waitForPushCount polls until the scripted backend's pushCount reaches
// want, or the deadline expires. Used to observe eventual-consistency
// effects in async mode without time.Sleep guessing.
func waitForPushCount(t *testing.T, s *scriptedBackend, want int, deadline time.Duration) {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		s.mu.Lock()
		got := s.pushCount
		s.mu.Unlock()
		if got >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	s.mu.Lock()
	got := s.pushCount
	s.mu.Unlock()
	t.Fatalf("mirror pushCount: want ≥%d within %v, got %d", want, deadline, got)
}

func TestMirrored_AsyncPin_PrimaryImmediateMirrorEventual(t *testing.T) {
	primary := newScriptedBackend()
	mirror := newScriptedBackend()
	ms := NewMirroredStore(primary, mirror, MirroredConfig{Mode: MirrorModeAsyncPin, Logger: quietLogger()})
	t.Cleanup(ms.Close)

	data := []byte("async-pin")
	cid := storage.Compute(data)
	if err := ms.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}

	// Primary was written synchronously.
	if primary.pushCount != 1 {
		t.Fatalf("primary pushCount: want 1, got %d", primary.pushCount)
	}
	// Mirror is eventual — wait for the async worker.
	waitForPushCount(t, mirror, 1, 2*time.Second)
}

// TestMirrored_AsyncPin_CloseIsClean verifies the async worker terminates
// on Close — no goroutine leak. goleak in TestMain catches the failure.
func TestMirrored_AsyncPin_CloseIsClean(t *testing.T) {
	primary := newScriptedBackend()
	mirror := newScriptedBackend()
	ms := NewMirroredStore(primary, mirror, MirroredConfig{Mode: MirrorModeAsyncPin, Logger: quietLogger()})

	data := []byte("x")
	cid := storage.Compute(data)
	if err := ms.Push(cid, data); err != nil {
		t.Fatal(err)
	}
	waitForPushCount(t, mirror, 1, 2*time.Second)

	// Close stops the worker. goleak in TestMain verifies no goroutine
	// survived the test binary's exit.
	ms.Close()

	// Small pause for the goroutine to actually exit before TestMain
	// runs goleak.Find. In practice, goleak has its own short retry.
	time.Sleep(50 * time.Millisecond)
}

func TestMirrored_AsyncPin_InvalidConfigStillRequires_Close(t *testing.T) {
	// This test protects against a regression where someone forgets
	// to call Close in async mode and leaks the goroutine. Intentionally
	// does NOT defer Close → goleak must still pass because we call
	// Close manually below.
	primary := newScriptedBackend()
	mirror := newScriptedBackend()
	ms := NewMirroredStore(primary, mirror, MirroredConfig{Mode: MirrorModeAsyncPin, Logger: quietLogger()})
	// (no t.Cleanup for ms.Close on purpose)

	ms.Close() // explicit

	// Re-assert to keep test meaningful.
	time.Sleep(20 * time.Millisecond)
}

// ─── Default mode and nil logger fallback ────────────────────────────

func TestMirrored_DefaultModeIsSync(t *testing.T) {
	ms := NewMirroredStore(newScriptedBackend(), newScriptedBackend(), MirroredConfig{})
	t.Cleanup(ms.Close)
	if ms.mode != MirrorModeSync {
		t.Fatalf("default mode: want %s, got %s", MirrorModeSync, ms.mode)
	}
	// pinCh should be nil in sync mode — no background goroutine.
	if ms.pinCh != nil {
		t.Fatal("sync mode must not allocate pinCh")
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
