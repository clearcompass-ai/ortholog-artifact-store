//go:build staging

package staging

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-artifact-store/internal/signers"
	"github.com/clearcompass-ai/ortholog-artifact-store/tests/conformance"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// Filebase S3 endpoint is region-less. Use us-east-1 as the signing
// region (Filebase accepts it; their docs use it in examples).
const filebaseS3Endpoint = "https://s3.filebase.com"

func newFilebaseS3Backend(t *testing.T, prefix string) backends.BackendProvider {
	t.Helper()
	if !filebaseConfigured() {
		t.Skipf("Filebase credentials not configured")
	}
	sigv4 := &signers.SigV4{
		AccessKey: os.Getenv("STAGING_FILEBASE_KEY"),
		SecretKey: os.Getenv("STAGING_FILEBASE_SECRET"),
		Region:    "us-east-1",
		Service:   "s3",
	}
	return backends.NewS3Backend(backends.S3Config{
		Endpoint:      filebaseS3Endpoint,
		Bucket:        os.Getenv("STAGING_FILEBASE_BUCKET"),
		Region:        "us-east-1",
		Prefix:        prefix,
		PathStyle:     true, // Filebase requires path-style
		RequestSigner: sigv4,
		URLSigner: &signers.BoundS3Presigner{
			Signer: sigv4, Endpoint: filebaseS3Endpoint,
		},
	})
}

// TestConformance_Filebase_S3 runs the full conformance suite against
// Filebase's S3-compatible API. This validates the S3Backend works
// against the S3-compatible vendor that ALSO happens to provide IPFS
// storage — a key deployment target for Ortholog.
func TestConformance_Filebase_S3(t *testing.T) {
	if !filebaseConfigured() {
		t.Skip("Filebase not configured for this run")
	}
	recordOp(t)
	prefix := randomPrefix(t)
	factory := func() backends.BackendProvider {
		return newFilebaseS3Backend(t, prefix)
	}

	conformance.RunBackendConformance(t, "filebase-s3", factory,
		conformance.Capabilities{
			SupportsDelete:        true,
			SupportsExpiry:        true,
			ExpectedResolveMethod: storage.MethodSignedURL,
		})
}

// TestFilebase_S3_Healthy localizes credential failures at the top.
func TestFilebase_S3_Healthy(t *testing.T) {
	if !filebaseConfigured() {
		t.Skip("Filebase not configured")
	}
	b := newFilebaseS3Backend(t, randomPrefix(t))
	recordOp(t)
	if err := b.Healthy(); err != nil {
		t.Fatalf("Filebase S3 Healthy: %v", err)
	}
}

// TestFilebase_S3_PresignedURLIsFetchable exercises the SigV4 presigner
// against Filebase's server-side verifier. Filebase's SigV4 may diverge
// subtly from AWS's; this test pins down compatibility.
func TestFilebase_S3_PresignedURLIsFetchable(t *testing.T) {
	if !filebaseConfigured() {
		t.Skip("Filebase not configured")
	}
	b := newFilebaseS3Backend(t, randomPrefix(t))

	data := []byte("staging-filebase-s3-presigned")
	cid := storage.Compute(data)
	if err := b.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}
	recordOp(t)

	cred, err := b.Resolve(cid, 120*time.Second)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	recordOp(t)

	got, err := fetchURLBytes(context.Background(), cred.URL)
	if err != nil {
		t.Fatalf("GET presigned URL: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("presigned URL bytes mismatch")
	}
}

// TestFilebase_S3_404NotFound validates that Filebase's S3 layer returns
// 404 (not 403) for missing objects in public-read buckets. The backend
// folds both into storage.ErrContentNotFound.
func TestFilebase_S3_404NotFound(t *testing.T) {
	if !filebaseConfigured() {
		t.Skip("Filebase not configured")
	}
	b := newFilebaseS3Backend(t, randomPrefix(t))
	cid := storage.Compute([]byte("filebase-never-pushed"))
	_, err := b.Fetch(cid)
	recordOp(t)
	if !errors.Is(err, storage.ErrContentNotFound) {
		t.Fatalf("Fetch missing: want ErrContentNotFound, got %v", err)
	}
}
