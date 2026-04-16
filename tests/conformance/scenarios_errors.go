package conformance

import (
	"errors"
	"testing"

	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

func runErrors(t *testing.T, factory Factory, caps Capabilities) {
	t.Run("fetch_missing_returns_not_found", func(t *testing.T) {
		b := factory()
		cid := storage.Compute([]byte("definitely-not-pushed"))

		_, err := b.Fetch(cid)
		if !errors.Is(err, storage.ErrContentNotFound) {
			t.Fatalf("Fetch missing: want ErrContentNotFound, got %v", err)
		}
	})

	t.Run("pin_missing_returns_not_found_or_is_tolerant", func(t *testing.T) {
		// Different backends handle pinning a missing object differently:
		// GCS/S3 return ErrContentNotFound (the PATCH/tagging call fails).
		// IPFS pin-add of an unpinned CID would fail (we haven't added it).
		// InMemoryBackend returns ErrContentNotFound from its SDK impl.
		// A backend that silently succeeds is acceptable (pin-on-missing
		// is already an unusual call pattern) but we require consistent
		// behavior: either nil or ErrContentNotFound, not a random error.
		b := factory()
		cid := storage.Compute([]byte("pin-missing"))

		err := b.Pin(cid)
		if err != nil && !errors.Is(err, storage.ErrContentNotFound) {
			t.Fatalf("Pin missing: want nil or ErrContentNotFound, got %v", err)
		}
	})

	if caps.SupportsDelete {
		t.Run("delete_missing_does_not_panic", func(t *testing.T) {
			// Some backends return nil, some return ErrContentNotFound.
			// Both are acceptable — we just require no panic.
			b := factory()
			cid := storage.Compute([]byte("delete-missing"))

			_ = b.Delete(cid)
		})
	}

	t.Run("healthy_returns_nil_for_working_backend", func(t *testing.T) {
		b := factory()
		if err := b.Healthy(); err != nil {
			t.Fatalf("Healthy on working backend returned error: %v", err)
		}
	})
}
