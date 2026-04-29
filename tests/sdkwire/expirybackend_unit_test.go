package sdkwire

import (
	"testing"
	"time"

	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// Direct unit cover for the expiryBackend test helper itself.
// The resolve_test.go suite exercises it through the full HTTP path;
// these checks pin the helper's inherent behavior so a regression
// in the helper doesn't masquerade as a wire-format bug.

func TestExpiryBackend_StampsExpiry(t *testing.T) {
	moment := time.Now().Add(1 * time.Hour).UTC().Truncate(time.Second)
	b := newExpiryBackend(moment)

	data := []byte("expirybackend/unit/stamp")
	cid := storage.Compute(data)
	if err := b.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}

	cred, err := b.Resolve(cid, time.Minute)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cred.Method != storage.MethodSignedURL {
		t.Fatalf("Method=%q, want MethodSignedURL", cred.Method)
	}
	if cred.Expiry == nil {
		t.Fatal("Expiry is nil; helper failed to stamp")
	}
	if !cred.Expiry.Equal(moment) {
		t.Fatalf("Expiry drift: in=%v out=%v", moment, *cred.Expiry)
	}
}

func TestExpiryBackend_DelegatesContentStore(t *testing.T) {
	// Push/Fetch/Exists/Delete must all work — the helper should not
	// shadow the embedded InMemoryBackend's storage methods.
	b := newExpiryBackend(time.Now())

	data := []byte("expirybackend/unit/delegation")
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
	got, err := b.Fetch(cid)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("Fetch returned wrong bytes")
	}
	if err := b.Delete(cid); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestExpiryBackend_HealthyPropagated(t *testing.T) {
	b := newExpiryBackend(time.Now())
	if err := b.Healthy(); err != nil {
		t.Fatalf("Healthy: %v", err)
	}
}
