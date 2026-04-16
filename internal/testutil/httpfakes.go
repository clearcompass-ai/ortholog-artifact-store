package testutil

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
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
type ObservedRequest struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   []byte
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
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.RawQuery,
		Header: r.Header.Clone(),
		Body:   body,
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
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.RawQuery,
		Header: r.Header.Clone(),
		Body:   body,
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

// ─────────────────────────────────────────────────────────────────────
// Kubo fake — mimics the Kubo RPC API surface that IPFSBackend uses.
// ─────────────────────────────────────────────────────────────────────

// KuboFake stands in for the Kubo RPC API. Stores objects by digest
// (not by multihash CID string — the fake's internal key is the raw
// 32-byte digest). Returns valid-looking CIDv1 strings from /api/v0/add.
type KuboFake struct {
	mu       sync.Mutex
	srv      *httptest.Server
	objects  map[string][]byte // key = hex(digest)
	pins     map[string]bool
	requests []ObservedRequest

	// Overrides for error simulation.
	AddStatus    int
	CatStatus    int
	PinStatus    int
	PinLsStatus  int
	IDStatus     int
	CorruptAddCID bool // if true, returns a CID whose digest does not match body
}

// NewKuboFake starts a Kubo RPC fake.
func NewKuboFake(t *testing.T) *KuboFake {
	t.Helper()
	f := &KuboFake{
		objects: make(map[string][]byte),
		pins:    make(map[string]bool),
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

// URL returns the base URL suitable as IPFSConfig.APIEndpoint.
func (f *KuboFake) URL() string { return f.srv.URL }

// Requests returns observed requests.
func (f *KuboFake) Requests() []ObservedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ObservedRequest, len(f.requests))
	copy(out, f.requests)
	return out
}

func (f *KuboFake) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, ObservedRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.RawQuery,
		Header: r.Header.Clone(),
		Body:   append([]byte(nil), body...),
	})
	f.mu.Unlock()

	switch r.URL.Path {
	case "/api/v0/add":
		f.handleAdd(w, r, body)
	case "/api/v0/cat":
		f.handleCat(w, r)
	case "/api/v0/pin/add":
		f.handlePinAdd(w, r)
	case "/api/v0/pin/ls":
		f.handlePinLs(w, r)
	case "/api/v0/id":
		if f.IDStatus != 0 {
			w.WriteHeader(f.IDStatus)
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"ID": "fake-kubo"})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *KuboFake) handleAdd(w http.ResponseWriter, r *http.Request, body []byte) {
	if f.AddStatus != 0 {
		w.WriteHeader(f.AddStatus)
		return
	}

	// Parse multipart form to extract the file bytes.
	ct := r.Header.Get("Content-Type")
	boundary := ""
	if idx := strings.Index(ct, "boundary="); idx >= 0 {
		boundary = ct[idx+len("boundary="):]
	}
	if boundary == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	mr := multipart.NewReader(strings.NewReader(string(body)), boundary)
	var fileBytes []byte
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if part.FormName() == "file" {
			fileBytes, _ = io.ReadAll(part)
			break
		}
	}
	if fileBytes == nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Compute digest and synthesize a CIDv1 string.
	digest := sha256DigestHex(fileBytes)
	f.mu.Lock()
	f.objects[digest] = fileBytes
	f.pins[digest] = true
	f.mu.Unlock()

	cidStr := syntheticCIDv1(fileBytes)
	if f.CorruptAddCID {
		// Return a CID for different bytes to exercise the backend's
		// digest-mismatch detection path.
		cidStr = syntheticCIDv1([]byte("corrupted-placeholder"))
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"Name": "artifact",
		"Hash": cidStr,
		"Size": fmt.Sprintf("%d", len(fileBytes)),
	})
}

func (f *KuboFake) handleCat(w http.ResponseWriter, r *http.Request) {
	if f.CatStatus != 0 {
		w.WriteHeader(f.CatStatus)
		return
	}
	cidStr := r.URL.Query().Get("arg")
	digest, err := digestFromSyntheticCID(cidStr)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("block was not found locally"))
		return
	}
	f.mu.Lock()
	data, ok := f.objects[digest]
	f.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("block was not found locally"))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (f *KuboFake) handlePinAdd(w http.ResponseWriter, r *http.Request) {
	if f.PinStatus != 0 {
		w.WriteHeader(f.PinStatus)
		return
	}
	cidStr := r.URL.Query().Get("arg")
	digest, err := digestFromSyntheticCID(cidStr)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	f.mu.Lock()
	if _, ok := f.objects[digest]; ok {
		f.pins[digest] = true
	}
	f.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (f *KuboFake) handlePinLs(w http.ResponseWriter, r *http.Request) {
	if f.PinLsStatus != 0 {
		w.WriteHeader(f.PinLsStatus)
		return
	}
	cidStr := r.URL.Query().Get("arg")
	digest, err := digestFromSyntheticCID(cidStr)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	f.mu.Lock()
	pinned := f.pins[digest]
	f.mu.Unlock()
	if !pinned {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"Keys": map[string]any{cidStr: map[string]string{"Type": "recursive"}}})
}

// sha256DigestHex returns hex(sha256(data)). Used as the fake's map key.
func sha256DigestHex(data []byte) string {
	sum := hashSHA256(data)
	return hex.EncodeToString(sum)
}

// syntheticCIDv1 builds a minimal CIDv1 string (base32 'b' prefix) that
// SDKCIDToIPFSPath/ExtractDigestFromIPFSCID can round-trip. It uses the
// same format: [0x01 version][0x55 raw codec][0x12 SHA-256][0x20 len][digest].
func syntheticCIDv1(data []byte) string {
	raw := []byte{0x01, 0x55, 0x12, 0x20}
	raw = append(raw, hashSHA256(data)...)
	return "b" + encBase32Lower(raw)
}

// digestFromSyntheticCID extracts hex(digest) from a CIDv1 string,
// mirroring the decode path in the real IPFS backend.
func digestFromSyntheticCID(cidStr string) (string, error) {
	if len(cidStr) < 2 || cidStr[0] != 'b' {
		return "", fmt.Errorf("unsupported CID prefix")
	}
	raw, err := decBase32Lower(cidStr[1:])
	if err != nil {
		return "", err
	}
	if len(raw) < 4+32 {
		return "", fmt.Errorf("CID too short")
	}
	return hex.EncodeToString(raw[4 : 4+32]), nil
}

const b32Alphabet = "abcdefghijklmnopqrstuvwxyz234567"

func encBase32Lower(data []byte) string {
	var sb strings.Builder
	sb.Grow((len(data)*8 + 4) / 5)
	var buf uint64
	var bits int
	for _, b := range data {
		buf = (buf << 8) | uint64(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			sb.WriteByte(b32Alphabet[(buf>>uint(bits))&0x1f])
		}
	}
	if bits > 0 {
		sb.WriteByte(b32Alphabet[(buf<<uint(5-bits))&0x1f])
	}
	return sb.String()
}

func decBase32Lower(s string) ([]byte, error) {
	var lookup [256]byte
	for i := range lookup {
		lookup[i] = 0xFF
	}
	for i, c := range b32Alphabet {
		lookup[c] = byte(i)
	}
	result := make([]byte, 0, len(s)*5/8)
	var buf uint64
	var bits int
	for _, c := range []byte(s) {
		if c == '=' {
			break
		}
		v := lookup[c]
		if v == 0xFF {
			return nil, fmt.Errorf("invalid base32 char %q", c)
		}
		buf = (buf << 5) | uint64(v)
		bits += 5
		if bits >= 8 {
			bits -= 8
			result = append(result, byte(buf>>uint(bits)))
		}
	}
	return result, nil
}
