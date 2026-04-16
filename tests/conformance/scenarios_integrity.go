package conformance

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/clearcompass-ai/ortholog-artifact-store/internal/testutil"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

func runIntegrity(t *testing.T, factory Factory, _ Capabilities) {
	// Exercise a variety of payload sizes that historically expose
	// off-by-one, alignment, or buffer-handling bugs in storage layers.
	// 0     → empty body
	// 1     → single byte
	// 32    → exactly digest-size (catches digest-vs-content confusion)
	// 1024  → small, typical
	// 65535 → just below a 16-bit boundary (network I/O chunk sizes)
	// 65536 → 64 KiB, another common boundary
	// 1MiB  → reasonable artifact, exercises chunked reads
	sizes := []int{0, 1, 32, 1024, 65535, 65536, 1 << 20}

	for _, size := range sizes {
		size := size
		t.Run(fmt.Sprintf("size_%d", size), func(t *testing.T) {
			b := factory()
			data := testutil.DeterministicBytes(int64(size), size)
			cid := storage.Compute(data)

			if err := b.Push(cid, data); err != nil {
				t.Fatalf("Push size=%d: %v", size, err)
			}
			got, err := b.Fetch(cid)
			if err != nil {
				t.Fatalf("Fetch size=%d: %v", size, err)
			}
			if !bytes.Equal(got, data) {
				// Don't print megabytes of hex in the failure message.
				wantDigest := sha256.Sum256(data)
				gotDigest := sha256.Sum256(got)
				t.Fatalf("byte mismatch at size=%d:\n  want_sha256=%x (len=%d)\n  got_sha256 =%x (len=%d)",
					size, wantDigest, len(data), gotDigest, len(got))
			}
		})
	}
}
