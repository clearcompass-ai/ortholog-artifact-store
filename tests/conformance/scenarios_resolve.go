package conformance

import (
	"testing"
	"time"

	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

func runResolve(t *testing.T, factory Factory, caps Capabilities) {
	t.Run("resolve_returns_expected_method", func(t *testing.T) {
		b := factory()
		data := []byte("resolve-method-check")
		cid := storage.Compute(data)

		if err := b.Push(cid, data); err != nil {
			t.Fatalf("Push failed: %v", err)
		}
		cred, err := b.Resolve(cid, time.Hour)
		if err != nil {
			t.Fatalf("Resolve failed: %v", err)
		}
		if cred.Method != caps.ExpectedResolveMethod {
			t.Fatalf("Resolve.Method:\n  want=%q\n  got =%q", caps.ExpectedResolveMethod, cred.Method)
		}
		if cred.URL == "" {
			t.Fatal("Resolve.URL is empty")
		}
	})

	if caps.SupportsExpiry {
		t.Run("resolve_returns_non_nil_expiry", func(t *testing.T) {
			b := factory()
			data := []byte("resolve-expiry")
			cid := storage.Compute(data)

			if err := b.Push(cid, data); err != nil {
				t.Fatalf("Push failed: %v", err)
			}
			before := time.Now()
			cred, err := b.Resolve(cid, 3600*time.Second)
			if err != nil {
				t.Fatalf("Resolve failed: %v", err)
			}
			if cred.Expiry == nil {
				t.Fatal("Resolve.Expiry is nil for a backend that claims to support expiry")
			}
			// Expiry should be after the request time + (duration - small slack).
			earliest := before.Add(3500 * time.Second)
			if cred.Expiry.Before(earliest) {
				t.Fatalf("Resolve.Expiry too early: want ≥%v, got %v", earliest, *cred.Expiry)
			}
		})

		t.Run("resolve_different_cids_produce_different_urls", func(t *testing.T) {
			b := factory()
			a := []byte("resolve-a")
			c := []byte("resolve-b")
			cidA := storage.Compute(a)
			cidB := storage.Compute(c)

			if err := b.Push(cidA, a); err != nil {
				t.Fatalf("Push A failed: %v", err)
			}
			if err := b.Push(cidB, c); err != nil {
				t.Fatalf("Push B failed: %v", err)
			}
			credA, err := b.Resolve(cidA, time.Hour)
			if err != nil {
				t.Fatalf("Resolve A failed: %v", err)
			}
			credB, err := b.Resolve(cidB, time.Hour)
			if err != nil {
				t.Fatalf("Resolve B failed: %v", err)
			}
			if credA.URL == credB.URL {
				t.Fatal("different CIDs produced the same Resolve URL")
			}
		})
	} else {
		t.Run("resolve_returns_nil_expiry", func(t *testing.T) {
			b := factory()
			data := []byte("no-expiry")
			cid := storage.Compute(data)

			if err := b.Push(cid, data); err != nil {
				t.Fatalf("Push failed: %v", err)
			}
			cred, err := b.Resolve(cid, time.Hour)
			if err != nil {
				t.Fatalf("Resolve failed: %v", err)
			}
			if cred.Expiry != nil {
				t.Fatalf("Resolve.Expiry should be nil for this backend, got %v", *cred.Expiry)
			}
		})
	}
}
