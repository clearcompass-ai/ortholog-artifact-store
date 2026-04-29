package testutil

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────
// GCS fake — mimics the GCS JSON API surface that GCSBackend uses.
// ─────────────────────────────────────────────────────────────────────

// GCSFake stands in for the GCS JSON API. It exposes URL() for backend
// construction and tracks each observed request so tests can assert on
// the exact method, path, headers, and body the backend emitted.
//
// Endpoints served (path-prefix match):
//
//	POST   /upload/storage/v1/b/{bucket}/o?uploadType=media&name=... → push
//	GET    /storage/v1/b/{bucket}/o/{name}?alt=media                  → fetch
//	GET    /storage/v1/b/{bucket}/o/{name}                            → exists / metadata
//	PATCH  /storage/v1/b/{bucket}/o/{name}                            → pin (metadata write)
//	DELETE /storage/v1/b/{bucket}/o/{name}                            → delete
//	GET    /storage/v1/b/{bucket}                                     → healthy
type GCSFake struct {
	mu       sync.Mutex
	srv      *httptest.Server
	objects  map[string][]byte // key = "bucket/object"
	metadata map[string]map[string]string
	requests []ObservedRequest

	// Overrides for specific request shapes — tests set these to
	// simulate errors from the backend.
	PushStatus    int // 0 = default 200
	FetchStatus   int // 0 = default 200/404
	ExistsStatus  int // 0 = default 200/404
	PinStatus     int // 0 = default 200
	DeleteStatus  int // 0 = default 200
	HealthyStatus int // 0 = default 200
}

// ObservedRequest captures one request for later assertions.
//
// Path is the decoded request path (Go's r.URL.Path) — convenient for
// "did this request hit /storage/v1/b/<bucket>/o/<name>" assertions.
// RawPath is the on-the-wire encoded form (Go's r.URL.EscapedPath()) —
// preserved separately so tests can pin URL-encoding behavior, e.g.
// confirming "/" in an object name is sent as "%2F" rather than as a
// literal slash.
type ObservedRequest struct {
	Method  string
	Path    string
	RawPath string
	Query   string
	Header  http.Header
	Body    []byte
}

// NewGCSFake starts an httptest.Server backed by the GCS JSON API.
func NewGCSFake(t *testing.T) *GCSFake {
	t.Helper()
	f := &GCSFake{
		objects:  make(map[string][]byte),
		metadata: make(map[string]map[string]string),
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

// URL returns the base URL of the fake (e.g., http://127.0.0.1:PPPP).
// GCSBackend uses storage.googleapis.com paths, so tests must construct
// a backend that points at this URL — see gcs_test.go for the pattern.
func (f *GCSFake) URL() string { return f.srv.URL }

// Requests returns a snapshot of observed requests.
func (f *GCSFake) Requests() []ObservedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ObservedRequest, len(f.requests))
	copy(out, f.requests)
	return out
}

// Put lets a test preload an object (simulating data written by another
// producer) so that Fetch/Exists/Resolve tests can exercise the read path.
func (f *GCSFake) Put(bucket, object string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[bucket+"/"+object] = append([]byte(nil), data...)
}

func (f *GCSFake) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, ObservedRequest{
		Method:  r.Method,
		Path:    r.URL.Path,
		RawPath: r.URL.EscapedPath(),
		Query:   r.URL.RawQuery,
		Header:  r.Header.Clone(),
		Body:    body,
	})
	f.mu.Unlock()

	path := r.URL.Path

	// Upload: POST /upload/storage/v1/b/{bucket}/o?uploadType=media&name=...
	if r.Method == http.MethodPost && strings.HasPrefix(path, "/upload/storage/v1/b/") {
		if f.PushStatus != 0 {
			w.WriteHeader(f.PushStatus)
			return
		}
		bucket := strings.TrimSuffix(strings.TrimPrefix(path, "/upload/storage/v1/b/"), "/o")
		object := r.URL.Query().Get("name")
		f.mu.Lock()
		f.objects[bucket+"/"+object] = body
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"name": object, "bucket": bucket})
		return
	}

	// Healthy check: GET /storage/v1/b/{bucket}
	if r.Method == http.MethodGet && strings.HasPrefix(path, "/storage/v1/b/") && !strings.Contains(strings.TrimPrefix(path, "/storage/v1/b/"), "/o") {
		if f.HealthyStatus != 0 {
			w.WriteHeader(f.HealthyStatus)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	// GET object metadata or media: /storage/v1/b/{bucket}/o/{name}[?alt=media]
	if strings.HasPrefix(path, "/storage/v1/b/") && strings.Contains(path, "/o/") {
		parts := strings.SplitN(strings.TrimPrefix(path, "/storage/v1/b/"), "/o/", 2)
		if len(parts) != 2 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		bucket, object := parts[0], parts[1]
		key := bucket + "/" + object

		switch r.Method {
		case http.MethodGet:
			if r.URL.Query().Get("alt") == "media" {
				if f.FetchStatus != 0 {
					w.WriteHeader(f.FetchStatus)
					return
				}
				f.mu.Lock()
				data, ok := f.objects[key]
				f.mu.Unlock()
				if !ok {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(data)
				return
			}
			if f.ExistsStatus != 0 {
				w.WriteHeader(f.ExistsStatus)
				return
			}
			f.mu.Lock()
			_, ok := f.objects[key]
			f.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"name": object})
			return

		case http.MethodPatch:
			if f.PinStatus != 0 {
				w.WriteHeader(f.PinStatus)
				return
			}
			f.mu.Lock()
			_, ok := f.objects[key]
			if ok {
				if f.metadata[key] == nil {
					f.metadata[key] = make(map[string]string)
				}
				f.metadata[key]["ortholog-pinned"] = "true"
			}
			f.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			return

		case http.MethodDelete:
			if f.DeleteStatus != 0 {
				w.WriteHeader(f.DeleteStatus)
				return
			}
			f.mu.Lock()
			delete(f.objects, key)
			f.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
	}

	w.WriteHeader(http.StatusNotFound)
}

// ─────────────────────────────────────────────────────────────────────
// S3 fake — mimics the S3 REST API surface that S3Backend uses.
// ─────────────────────────────────────────────────────────────────────

// S3Fake stands in for an S3-compatible endpoint. Path-style only (the
// virtual-hosted form requires DNS rewriting). Tests that want to
// exercise virtual-hosted code paths do so with real httptest + DNS
// magic, which is out of scope for Wave 1.
type S3Fake struct {
	mu       sync.Mutex
	srv      *httptest.Server
	objects  map[string][]byte
	tags     map[string]map[string]string
	requests []ObservedRequest

	PushStatus    int
	FetchStatus   int
	ExistsStatus  int
	PinStatus     int
	DeleteStatus  int
	HealthyStatus int
}

// NewS3Fake starts a path-style S3-compatible fake.
func NewS3Fake(t *testing.T) *S3Fake {
	t.Helper()
	f := &S3Fake{
		objects: make(map[string][]byte),
		tags:    make(map[string]map[string]string),
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

// URL returns the base URL suitable as S3Config.Endpoint with PathStyle=true.
func (f *S3Fake) URL() string { return f.srv.URL }

// Requests returns observed requests.
func (f *S3Fake) Requests() []ObservedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ObservedRequest, len(f.requests))
	copy(out, f.requests)
	return out
}

// Put preloads an object.
func (f *S3Fake) Put(bucket, key string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[bucket+"/"+key] = append([]byte(nil), data...)
}

func (f *S3Fake) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, ObservedRequest{
		Method:  r.Method,
		Path:    r.URL.Path,
		RawPath: r.URL.EscapedPath(),
		Query:   r.URL.RawQuery,
		Header:  r.Header.Clone(),
		Body:    body,
	})
	f.mu.Unlock()

	// Path: /{bucket}/{key...}  (path-style)
	// Bucket HEAD / GET (no key)  → health
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	parts := strings.SplitN(path, "/", 2)
	bucket := parts[0]
	var key string
	if len(parts) == 2 {
		key = parts[1]
	}

	// Bucket-level request → health.
	if key == "" {
		if f.HealthyStatus != 0 {
			w.WriteHeader(f.HealthyStatus)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	fullKey := bucket + "/" + key

	// Tagging subresource.
	if r.URL.Query().Get("tagging") != "" || strings.HasSuffix(r.URL.RawQuery, "tagging") {
		if f.PinStatus != 0 {
			w.WriteHeader(f.PinStatus)
			return
		}
		f.mu.Lock()
		_, ok := f.objects[fullKey]
		if ok {
			if f.tags[fullKey] == nil {
				f.tags[fullKey] = make(map[string]string)
			}
			f.tags[fullKey]["ortholog-pinned"] = "true"
		}
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	switch r.Method {
	case http.MethodPut:
		if f.PushStatus != 0 {
			w.WriteHeader(f.PushStatus)
			return
		}
		f.mu.Lock()
		f.objects[fullKey] = body
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		if f.FetchStatus != 0 {
			w.WriteHeader(f.FetchStatus)
			return
		}
		f.mu.Lock()
		data, ok := f.objects[fullKey]
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	case http.MethodHead:
		if f.ExistsStatus != 0 {
			w.WriteHeader(f.ExistsStatus)
			return
		}
		f.mu.Lock()
		_, ok := f.objects[fullKey]
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		if f.DeleteStatus != 0 {
			w.WriteHeader(f.DeleteStatus)
			return
		}
		f.mu.Lock()
		delete(f.objects, fullKey)
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
