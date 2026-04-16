package conformance

import (
	"bytes"
	"fmt"
	"sync"
	"testing"

	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

func runConcurrent(t *testing.T, factory Factory, _ Capabilities) {
	t.Run("parallel_push_distinct_cids", func(t *testing.T) {
		b := factory()
		const N = 32

		// Prepare N distinct payloads.
		payloads := make([][]byte, N)
		cids := make([]storage.CID, N)
		for i := 0; i < N; i++ {
			payloads[i] = []byte(fmt.Sprintf("parallel-push-%d", i))
			cids[i] = storage.Compute(payloads[i])
		}

		// Push concurrently.
		var wg sync.WaitGroup
		errs := make([]error, N)
		for i := 0; i < N; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				errs[i] = b.Push(cids[i], payloads[i])
			}(i)
		}
		wg.Wait()

		for i, err := range errs {
			if err != nil {
				t.Fatalf("parallel Push[%d] failed: %v", i, err)
			}
		}

		// Every object must be retrievable with byte-exact content.
		for i := 0; i < N; i++ {
			got, err := b.Fetch(cids[i])
			if err != nil {
				t.Fatalf("Fetch[%d] failed: %v", i, err)
			}
			if !bytes.Equal(got, payloads[i]) {
				t.Fatalf("Fetch[%d] wrong bytes", i)
			}
		}
	})

	t.Run("parallel_push_same_cid_idempotent", func(t *testing.T) {
		b := factory()
		data := []byte("parallel-same-cid")
		cid := storage.Compute(data)
		const N = 16

		var wg sync.WaitGroup
		errs := make([]error, N)
		for i := 0; i < N; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				errs[i] = b.Push(cid, data)
			}(i)
		}
		wg.Wait()

		for i, err := range errs {
			if err != nil {
				t.Fatalf("idempotent parallel Push[%d] failed: %v", i, err)
			}
		}

		got, err := b.Fetch(cid)
		if err != nil {
			t.Fatalf("Fetch failed: %v", err)
		}
		if !bytes.Equal(got, data) {
			t.Fatal("bytes corrupted after idempotent parallel push")
		}
	})

	t.Run("parallel_fetch_is_safe", func(t *testing.T) {
		b := factory()
		data := []byte("parallel-fetch")
		cid := storage.Compute(data)
		if err := b.Push(cid, data); err != nil {
			t.Fatalf("Push failed: %v", err)
		}

		const N = 64
		var wg sync.WaitGroup
		results := make([][]byte, N)
		errs := make([]error, N)
		for i := 0; i < N; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				results[i], errs[i] = b.Fetch(cid)
			}(i)
		}
		wg.Wait()

		for i := 0; i < N; i++ {
			if errs[i] != nil {
				t.Fatalf("Fetch[%d] failed: %v", i, errs[i])
			}
			if !bytes.Equal(results[i], data) {
				t.Fatalf("Fetch[%d] wrong bytes", i)
			}
		}
	})
}
