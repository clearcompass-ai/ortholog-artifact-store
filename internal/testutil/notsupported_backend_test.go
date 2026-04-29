package testutil

import (
	"errors"
	"testing"
	"time"

	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// Direct unit cover for NotSupportedBackend. The conformance integration
// (tests/conformance/suite_notsupported_test.go) exercises it through
// the full RunBackendConformance matrix; these tests pin the per-method
// invariants in isolation so a regression surfaces with a precise name.

func TestNotSupportedBackend_DeleteReturnsErrNotSupported(t *testing.T) {
	b := NewNotSupportedBackend()
	cid := storage.Compute([]byte("notsupported/delete"))
	if err := b.Push(cid, []byte("notsupported/delete")); err != nil {
		t.Fatalf("Push: %v", err)
	}
	err := b.Delete(cid)
	if !errors.Is(err, storage.ErrNotSupported) {
		t.Fatalf("Delete: err=%v, want ErrNotSupported", err)
	}
}

func TestNotSupportedBackend_PushFetchRoundTrip(t *testing.T) {
	b := NewNotSupportedBackend()
	data := []byte("notsupported/push-fetch")
	cid := storage.Compute(data)
	if err := b.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}
	got, err := b.Fetch(cid)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("bytes drift: in=%q out=%q", data, got)
	}
}

func TestNotSupportedBackend_FetchMissingReturnsContentNotFound(t *testing.T) {
	b := NewNotSupportedBackend()
	cid := storage.Compute([]byte("notsupported/missing"))
	_, err := b.Fetch(cid)
	if !errors.Is(err, storage.ErrContentNotFound) {
		t.Fatalf("Fetch missing: err=%v, want ErrContentNotFound", err)
	}
}

func TestNotSupportedBackend_ExistsAfterPush(t *testing.T) {
	b := NewNotSupportedBackend()
	data := []byte("notsupported/exists")
	cid := storage.Compute(data)
	if err := b.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}
	exists, err := b.Exists(cid)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatal("Exists returned false after Push")
	}
}

func TestNotSupportedBackend_ExistsBeforePush(t *testing.T) {
	b := NewNotSupportedBackend()
	cid := storage.Compute([]byte("notsupported/never"))
	exists, err := b.Exists(cid)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Fatal("Exists returned true for un-pushed CID")
	}
}

func TestNotSupportedBackend_PinAfterPush(t *testing.T) {
	b := NewNotSupportedBackend()
	data := []byte("notsupported/pin")
	cid := storage.Compute(data)
	if err := b.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if err := b.Pin(cid); err != nil {
		t.Fatalf("Pin: %v", err)
	}
}

func TestNotSupportedBackend_ResolveReturnsMethodDirect(t *testing.T) {
	b := NewNotSupportedBackend()
	data := []byte("notsupported/resolve")
	cid := storage.Compute(data)
	if err := b.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}
	cred, err := b.Resolve(cid, time.Hour)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cred.Method != storage.MethodDirect {
		t.Fatalf("Method=%q, want MethodDirect", cred.Method)
	}
	if cred.Expiry != nil {
		t.Fatalf("Expiry=%v, want nil for MethodDirect", cred.Expiry)
	}
}

func TestNotSupportedBackend_HealthyAlwaysNil(t *testing.T) {
	b := NewNotSupportedBackend()
	if err := b.Healthy(); err != nil {
		t.Fatalf("Healthy: %v", err)
	}
}
