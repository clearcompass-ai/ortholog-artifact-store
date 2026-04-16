package backends

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/clearcompass-ai/ortholog-artifact-store/internal/testutil"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// ─── Test signers ────────────────────────────────────────────────────

// capturingS3Signer records every request it's asked to sign, so tests
// can assert the backend actually invoked the signer. It always succeeds
// — signature correctness is not the backend's responsibility.
type capturingS3Signer struct {
	signedRequests []string // "METHOD path"
}

func (c *capturingS3Signer) SignRequest(req *http.Request) error {
	c.signedRequests = append(c.signedRequests, req.Method+" "+req.URL.Path)
	// Set a marker header so the fake can observe the signer ran.
	req.Header.Set("X-Signed-By-Test", "true")
	return nil
}

type fakeS3Presigner struct{}

func (fakeS3Presigner) PresignGetObject(bucket, key string, expiry time.Duration) (string, error) {
	return fmt.Sprintf("https://signed.example/%s/%s?e=%d",
		bucket, key, int(expiry.Seconds())), nil
}

type brokenS3Presigner struct{}

func (brokenS3Presigner) PresignGetObject(_, _ string, _ time.Duration) (string, error) {
	return "", errors.New("presigner exploded")
}

// newS3BackendWithFake wires an S3Backend at the fake server's URL in
// path-style mode with a capturing signer.
func newS3BackendWithFake(t *testing.T, fake *testutil.S3Fake) (*S3Backend, *capturingS3Signer) {
	t.Helper()
	signer := &capturingS3Signer{}
	b := NewS3Backend(S3Config{
		Endpoint:      fake.URL(),
		Bucket:        "test-bucket",
		Region:        "us-east-1",
		Prefix:        "artifacts/",
		PathStyle:     true,
		RequestSigner: signer,
		URLSigner:     fakeS3Presigner{},
	})
	return b, signer
}

// ─── Lifecycle tests ─────────────────────────────────────────────────

func TestS3_Push_PathStyleURLAndSigned(t *testing.T) {
	fake := testutil.NewS3Fake(t)
	b, signer := newS3BackendWithFake(t, fake)

	data := []byte("s3-push-wire")
	cid := storage.Compute(data)

	if err := b.Push(cid, data); err != nil {
		t.Fatalf("Push: %v", err)
	}

	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("want 1 request, got %d", len(reqs))
	}
	r := reqs[0]

	if r.Method != http.MethodPut {
		t.Errorf("method: want PUT, got %s", r.Method)
	}
	wantPath := "/test-bucket/artifacts/" + cid.String()
	if r.Path != wantPath {
		t.Errorf("path: want %s, got %s", wantPath, r.Path)
	}
	if !bytes.Equal(r.Body, data) {
		t.Errorf("body: want %x, got %x", data, r.Body)
	}
	if r.Header.Get("X-Signed-By-Test") != "true" {
		t.Error("signer was not invoked for Push")
	}
	if len(signer.signedRequests) != 1 {
		t.Errorf("signer invocations: want 1, got %d", len(signer.signedRequests))
	}
}

func TestS3_Fetch_ReturnsBytes(t *testing.T) {
	fake := testutil.NewS3Fake(t)
	b, _ := newS3BackendWithFake(t, fake)
	data := []byte("s3-fetch")
	cid := storage.Compute(data)
	fake.Put("test-bucket", "artifacts/"+cid.String(), data)

	got, err := b.Fetch(cid)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("bytes mismatch")
	}
}

func TestS3_Fetch_404MapsToErrContentNotFound(t *testing.T) {
	fake := testutil.NewS3Fake(t)
	b, _ := newS3BackendWithFake(t, fake)
	cid := storage.Compute([]byte("missing"))

	_, err := b.Fetch(cid)
	if !errors.Is(err, storage.ErrContentNotFound) {
		t.Fatalf("Fetch missing: want ErrContentNotFound, got %v", err)
	}
}

// Documented quirk: S3 treats 403 like 404 for missing objects when the
// bucket-policy denies listing. The backend collapses both to ErrContentNotFound.
// This test pins that behavior.
func TestS3_Fetch_403AlsoMapsToErrContentNotFound(t *testing.T) {
	fake := testutil.NewS3Fake(t)
	fake.FetchStatus = http.StatusForbidden
	b, _ := newS3BackendWithFake(t, fake)
	cid := storage.Compute([]byte("forbidden"))

	_, err := b.Fetch(cid)
	if !errors.Is(err, storage.ErrContentNotFound) {
		t.Fatalf("Fetch 403: want ErrContentNotFound (backend quirk), got %v", err)
	}
}

func TestS3_Exists_HEAD(t *testing.T) {
	fake := testutil.NewS3Fake(t)
	b, _ := newS3BackendWithFake(t, fake)
	data := []byte("exists")
	cid := storage.Compute(data)
	fake.Put("test-bucket", "artifacts/"+cid.String(), data)

	exists, err := b.Exists(cid)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatal("Exists returned false for preloaded object")
	}

	// Verify the request was HEAD, not GET.
	reqs := fake.Requests()
	if reqs[0].Method != http.MethodHead {
		t.Fatalf("Exists method: want HEAD, got %s", reqs[0].Method)
	}
}

func TestS3_Exists_404(t *testing.T) {
	fake := testutil.NewS3Fake(t)
	b, _ := newS3BackendWithFake(t, fake)

	exists, err := b.Exists(storage.Compute([]byte("nope")))
	if err != nil {
		t.Fatalf("Exists missing: %v", err)
	}
	if exists {
		t.Fatal("Exists true for missing")
	}
}

func TestS3_Pin_UsesTaggingSubresource(t *testing.T) {
	fake := testutil.NewS3Fake(t)
	b, _ := newS3BackendWithFake(t, fake)
	data := []byte("pin")
	cid := storage.Compute(data)
	fake.Put("test-bucket", "artifacts/"+cid.String(), data)

	if err := b.Pin(cid); err != nil {
		t.Fatalf("Pin: %v", err)
	}

	// Find a PUT request whose path contains ?tagging.
	var tagged bool
	for _, r := range fake.Requests() {
		if r.Method == http.MethodPut && (r.Query == "tagging" || strings.HasSuffix(r.Query, "tagging")) {
			tagged = true
			if !strings.Contains(string(r.Body), "ortholog-pinned") {
				t.Errorf("Pin body missing 'ortholog-pinned' marker: %s", r.Body)
			}
			if ct := r.Header.Get("Content-Type"); ct != "application/xml" {
				t.Errorf("Pin Content-Type: want application/xml, got %q", ct)
			}
		}
	}
	if !tagged {
		t.Fatal("no tagging PUT observed for Pin")
	}
}

func TestS3_Delete(t *testing.T) {
	fake := testutil.NewS3Fake(t)
	b, _ := newS3BackendWithFake(t, fake)
	data := []byte("delete")
	cid := storage.Compute(data)
	fake.Put("test-bucket", "artifacts/"+cid.String(), data)

	if err := b.Delete(cid); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	reqs := fake.Requests()
	if last := reqs[len(reqs)-1]; last.Method != http.MethodDelete {
		t.Fatalf("last method: want DELETE, got %s", last.Method)
	}
}

func TestS3_Healthy_OK(t *testing.T) {
	fake := testutil.NewS3Fake(t)
	b, _ := newS3BackendWithFake(t, fake)
	if err := b.Healthy(); err != nil {
		t.Fatalf("Healthy: %v", err)
	}
}

func TestS3_Healthy_5xxReturnsError(t *testing.T) {
	fake := testutil.NewS3Fake(t)
	fake.HealthyStatus = http.StatusInternalServerError
	b, _ := newS3BackendWithFake(t, fake)
	if err := b.Healthy(); err == nil {
		t.Fatal("Healthy 500: want error, got nil")
	}
}

// ─── Resolve ─────────────────────────────────────────────────────────

func TestS3_Resolve_ReturnsMethodSignedURL(t *testing.T) {
	fake := testutil.NewS3Fake(t)
	b, _ := newS3BackendWithFake(t, fake)
	cid := storage.Compute([]byte("resolve"))

	cred, err := b.Resolve(cid, 1800*time.Second)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cred.Method != storage.MethodSignedURL {
		t.Fatalf("Method: want %s, got %s", storage.MethodSignedURL, cred.Method)
	}
	if cred.Expiry == nil {
		t.Fatal("Expiry must be non-nil for signed URL")
	}
	if !strings.Contains(cred.URL, "test-bucket") {
		t.Fatalf("URL missing bucket: %s", cred.URL)
	}
}

func TestS3_Resolve_NoSignerReturnsError(t *testing.T) {
	b := NewS3Backend(S3Config{Bucket: "t", Region: "us-east-1", Endpoint: "http://unused"})
	_, err := b.Resolve(storage.Compute([]byte("x")), time.Hour)
	if err == nil {
		t.Fatal("Resolve without signer: want error, got nil")
	}
}

func TestS3_Resolve_SignerErrorPropagates(t *testing.T) {
	b := NewS3Backend(S3Config{
		Bucket: "t", Region: "us-east-1",
		Endpoint:  "http://unused",
		URLSigner: brokenS3Presigner{},
	})
	_, err := b.Resolve(storage.Compute([]byte("x")), time.Hour)
	if err == nil {
		t.Fatal("Resolve with broken presigner: want error, got nil")
	}
	if !strings.Contains(err.Error(), "presigner exploded") {
		t.Fatalf("presigner error not propagated: %v", err)
	}
}

// ─── Endpoint computation ────────────────────────────────────────────

func TestS3_DefaultEndpoint_ComputedFromRegion(t *testing.T) {
	// When Endpoint is empty, the backend should compute
	// https://s3.{region}.amazonaws.com.
	b := NewS3Backend(S3Config{Bucket: "t", Region: "eu-west-2"})
	if !strings.Contains(b.cfg.Endpoint, "s3.eu-west-2.amazonaws.com") {
		t.Fatalf("default endpoint wrong: %s", b.cfg.Endpoint)
	}
}
