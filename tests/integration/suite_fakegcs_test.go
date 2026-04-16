//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-artifact-store/tests/conformance"
	"github.com/clearcompass-ai/ortholog-artifact-store/tests/integration/containers"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// fakeGCSURLSigner produces deterministic URLs pointing at the running
// fake-gcs-server. The fake accepts these without signature checking,
// so the conformance suite can validate URL shape and method dispatch
// without needing a real GCP private key.
type fakeGCSURLSigner struct {
	endpoint string
}

func (f *fakeGCSURLSigner) SignURL(bucket, object string, expiry time.Duration) (string, error) {
	return fmt.Sprintf("%s/storage/v1/b/%s/o/%s?alt=media&expiry=%d",
		f.endpoint, bucket, object, int(expiry.Seconds())), nil
}

// TestConformance_FakeGCS runs the full suite against GCSBackend pointed
// at fake-gcs-server. This validates the wire format and error
// classification against the community's GCS emulator, which tracks the
// real GCS JSON API closely.
func TestConformance_FakeGCS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	fg := containers.StartFakeGCS(t, ctx)

	conformance.RunBackendConformance(t, "fakegcs",
		func() backends.BackendProvider {
			return backends.NewGCSBackend(backends.GCSConfig{
				Bucket:    fg.Bucket,
				Prefix:    randomPrefix(t),
				BaseURL:   fg.Endpoint,
				TokenFunc: func() (string, error) { return "fake-gcs-ignored-token", nil },
				URLSigner: &fakeGCSURLSigner{endpoint: fg.Endpoint},
			})
		},
		conformance.Capabilities{
			SupportsDelete:        true,
			SupportsExpiry:        true,
			ExpectedResolveMethod: storage.MethodSignedURL,
		},
	)
}

// TestFakeGCS_Healthy localizes container-readiness failures.
func TestFakeGCS_Healthy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fg := containers.StartFakeGCS(t, ctx)
	b := backends.NewGCSBackend(backends.GCSConfig{
		Bucket:  fg.Bucket,
		BaseURL: fg.Endpoint,
	})
	if err := b.Healthy(); err != nil {
		t.Fatalf("FakeGCS Healthy: %v", err)
	}
}
