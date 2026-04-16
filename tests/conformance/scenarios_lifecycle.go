package conformance

import (
	"bytes"
	"errors"
	"testing"

	"github.com/clearcompass-ai/ortholog-artifact-store/internal/testutil"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

func runLifecycle(t *testing.T, factory Factory, caps Capabilities) {
	t.Run("push_then_fetch_returns_same_bytes", func(t *testing.T) {
		b := factory()
		data := []byte("lifecycle-test-payload")
		cid := storage.Compute(data)

		if err := b.Push(cid, data); err != nil {
			t.Fatalf("Push failed: %v", err)
		}
		got, err := b.Fetch(cid)
		if err != nil {
			t.Fatalf("Fetch failed: %v", err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("Fetch returned wrong bytes:\n  want=%x\n  got =%x", data, got)
		}
	})

	t.Run("exists_false_before_push", func(t *testing.T) {
		b := factory()
		cid := storage.Compute([]byte("not-yet-pushed"))

		exists, err := b.Exists(cid)
		if err != nil {
			t.Fatalf("Exists failed: %v", err)
		}
		if exists {
			t.Fatal("Exists returned true for CID that was never pushed")
		}
	})

	t.Run("exists_true_after_push", func(t *testing.T) {
		b := factory()
		data := []byte("exists-after-push")
		cid := storage.Compute(data)

		if err := b.Push(cid, data); err != nil {
			t.Fatalf("Push failed: %v", err)
		}
		exists, err := b.Exists(cid)
		if err != nil {
			t.Fatalf("Exists failed: %v", err)
		}
		if !exists {
			t.Fatal("Exists returned false for CID that was just pushed")
		}
	})

	t.Run("push_is_idempotent", func(t *testing.T) {
		b := factory()
		data := []byte("idempotent")
		cid := storage.Compute(data)

		if err := b.Push(cid, data); err != nil {
			t.Fatalf("first Push failed: %v", err)
		}
		if err := b.Push(cid, data); err != nil {
			t.Fatalf("second Push (idempotent) failed: %v", err)
		}
		got, err := b.Fetch(cid)
		if err != nil {
			t.Fatalf("Fetch after idempotent Push failed: %v", err)
		}
		if !bytes.Equal(got, data) {
			t.Fatal("bytes corrupted after idempotent push")
		}
	})

	t.Run("pin_on_existing_object_succeeds", func(t *testing.T) {
		b := factory()
		data := []byte("pin-me")
		cid := storage.Compute(data)

		if err := b.Push(cid, data); err != nil {
			t.Fatalf("Push failed: %v", err)
		}
		if err := b.Pin(cid); err != nil {
			t.Fatalf("Pin failed: %v", err)
		}
	})

	t.Run("pin_is_idempotent", func(t *testing.T) {
		b := factory()
		data := []byte("pin-twice")
		cid := storage.Compute(data)

		if err := b.Push(cid, data); err != nil {
			t.Fatalf("Push failed: %v", err)
		}
		if err := b.Pin(cid); err != nil {
			t.Fatalf("first Pin failed: %v", err)
		}
		if err := b.Pin(cid); err != nil {
			t.Fatalf("second Pin (idempotent) failed: %v", err)
		}
	})

	if caps.SupportsDelete {
		t.Run("delete_removes_object", func(t *testing.T) {
			b := factory()
			data := []byte("delete-me")
			cid := storage.Compute(data)

			if err := b.Push(cid, data); err != nil {
				t.Fatalf("Push failed: %v", err)
			}
			if err := b.Delete(cid); err != nil {
				t.Fatalf("Delete failed: %v", err)
			}
			exists, err := b.Exists(cid)
			if err != nil {
				t.Fatalf("Exists after Delete failed: %v", err)
			}
			if exists {
				t.Fatal("Exists returned true after Delete")
			}
		})

		t.Run("fetch_after_delete_returns_not_found", func(t *testing.T) {
			b := factory()
			data := []byte("delete-then-fetch")
			cid := storage.Compute(data)

			if err := b.Push(cid, data); err != nil {
				t.Fatalf("Push failed: %v", err)
			}
			if err := b.Delete(cid); err != nil {
				t.Fatalf("Delete failed: %v", err)
			}
			_, err := b.Fetch(cid)
			if !errors.Is(err, storage.ErrContentNotFound) {
				t.Fatalf("Fetch after Delete: want ErrContentNotFound, got %v", err)
			}
		})
	} else {
		t.Run("delete_returns_not_supported", func(t *testing.T) {
			b := factory()
			data := []byte("no-delete-here")
			cid := storage.Compute(data)

			// Push first so we're not masking a not-found error.
			if err := b.Push(cid, data); err != nil {
				t.Fatalf("Push failed: %v", err)
			}
			err := b.Delete(cid)
			if !errors.Is(err, storage.ErrNotSupported) {
				t.Fatalf("Delete: want ErrNotSupported, got %v", err)
			}
		})
	}

	t.Run("known_vectors_round_trip", func(t *testing.T) {
		// Every known CID vector round-trips through this backend.
		// If the SDK's Compute() ever changes algorithm, this fails loudly.
		for _, v := range testutil.KnownVectors {
			v := v
			t.Run(v.Name, func(t *testing.T) {
				b := factory()
				cid := v.ComputeCID()

				// Sanity check: the SDK's computed digest matches our
				// hardcoded expectation.
				if !bytes.Equal(cid.Digest, v.MustDigestBytes()) {
					t.Fatalf("SDK digest drift for vector %q:\n  want=%s\n  got =%x",
						v.Name, v.DigestHex, cid.Digest)
				}

				if err := b.Push(cid, v.Plaintext); err != nil {
					t.Fatalf("Push failed: %v", err)
				}
				got, err := b.Fetch(cid)
				if err != nil {
					t.Fatalf("Fetch failed: %v", err)
				}
				if !bytes.Equal(got, v.Plaintext) {
					t.Fatalf("round-trip mismatch for vector %q", v.Name)
				}
			})
		}
	})
}
