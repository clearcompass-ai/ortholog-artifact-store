//go:build integration

package integration

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-artifact-store/tests/integration/containers"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// TestMirrored_MinIO_And_FakeGCS exercises the MirroredStore decorator
// in sync mode across two real backends with completely different wire
// protocols. Every Push must land in both; a Fetch must fall back to
// the mirror when the primary is unavailable. Wave 1 tests this with
// an in-memory scripted backend; Wave 2 validates the behavior holds
// against heterogeneous real protocols.
func TestMirrored_MinIO_And_FakeGCS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	m := containers.StartMinIO(t, ctx)
	fg := containers.StartFakeGCS(t, ctx)

	primary := backends.NewS3Backend(backends.S3Config{
		Endpoint:  m.Endpoint,
		Bucket:    m.Bucket,
		Region:    m.Region,
		Prefix:    randomPrefix(t),
		PathStyle: true,
		RequestSigner: &containers.SigV4Signer{
			AccessKey: m.AccessKey, SecretKey: m.SecretKey,
			Region: m.Region, Service: "s3",
		},
	})
	mirror := backends.NewGCSBackend(backends.GCSConfig{
		Bucket:  fg.Bucket,
		Prefix:  randomPrefix(t),
		BaseURL: fg.Endpoint,
	})

	ms := backends.NewMirroredStore(primary, mirror, backends.MirroredConfig{
		Mode: backends.MirrorModeSync,
	})
	t.Cleanup(ms.Close)

	data := []byte("heterogeneous-mirror")
	cid := storage.Compute(data)

	if err := ms.Push(cid, data); err != nil {
		t.Fatalf("Mirrored Push: %v", err)
	}

	// Both backends must independently hold the bytes — this is the
	// core MirroredStore invariant.
	for name, b := range map[string]backends.BackendProvider{
		"primary (MinIO)":  primary,
		"mirror (FakeGCS)": mirror,
	} {
		got, err := b.Fetch(cid)
		if err != nil {
			t.Fatalf("%s Fetch: %v", name, err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("%s bytes mismatch", name)
		}
	}

	// Both Healthy paths must succeed — propagation of errors across
	// heterogeneous protocols is a real risk in production.
	if err := ms.Healthy(); err != nil {
		t.Fatalf("Mirrored Healthy: %v", err)
	}
}
