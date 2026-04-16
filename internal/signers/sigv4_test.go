package signers

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestSigV4_SignRequest_PopulatesHeaders is a smoke test that the signer
// runs end-to-end: canonical-request assembly, key derivation, HMAC chain,
// and Authorization header emission. The signature itself isn't verified
// here (that's what Wave 2 MinIO and Wave 3 real AWS are for); we only
// assert the signer ran without error and produced the header set SigV4
// consumers expect.
func TestSigV4_SignRequest_PopulatesHeaders(t *testing.T) {
	s := &SigV4{
		AccessKey: "AKIAIOSFODNN7EXAMPLE",
		SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		Region:    "us-east-1",
		Service:   "s3",
	}

	req, err := http.NewRequest(http.MethodPut,
		"https://examplebucket.s3.amazonaws.com/test.txt",
		strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	if err := s.SignRequest(req); err != nil {
		t.Fatalf("SignRequest: %v", err)
	}

	for _, h := range []string{"Authorization", "X-Amz-Date", "X-Amz-Content-Sha256"} {
		if req.Header.Get(h) == "" {
			t.Errorf("missing required header %s", h)
		}
	}

	auth := req.Header.Get("Authorization")
	for _, prefix := range []string{
		"AWS4-HMAC-SHA256",
		"Credential=AKIAIOSFODNN7EXAMPLE",
		"SignedHeaders=",
		"Signature=",
	} {
		if !strings.Contains(auth, prefix) {
			t.Errorf("Authorization missing %q: %s", prefix, auth)
		}
	}

	// Body must be rewound for the actual request send.
	body, _ := io.ReadAll(req.Body)
	if !bytes.Equal(body, []byte("hello")) {
		t.Errorf("body not rewound after signing: %q", body)
	}
}

// TestSigV4_PresignGetObject_URLShape validates the presigner emits the
// canonical query parameters AWS SigV4 query signing requires. Real-URL
// validity is proven in Wave 3 (TestAWS_S3_PresignedURLIsFetchable).
func TestSigV4_PresignGetObject_URLShape(t *testing.T) {
	s := &SigV4{
		AccessKey: "AKIAIOSFODNN7EXAMPLE",
		SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		Region:    "us-east-1",
		Service:   "s3",
	}

	url, err := s.PresignGetObject(
		"https://s3.us-east-1.amazonaws.com",
		"my-bucket", "path/to/object.bin",
		15*time.Minute,
	)
	if err != nil {
		t.Fatalf("PresignGetObject: %v", err)
	}

	for _, p := range []string{
		"X-Amz-Algorithm=AWS4-HMAC-SHA256",
		"X-Amz-Credential=",
		"X-Amz-Date=",
		"X-Amz-Expires=900",
		"X-Amz-SignedHeaders=host",
		"X-Amz-Signature=",
		"/my-bucket/path/to/object.bin",
	} {
		if !strings.Contains(url, p) {
			t.Errorf("presigned URL missing %q\n  got: %s", p, url)
		}
	}
}

func TestSigV4_PresignGetObject_RejectsBadEndpoint(t *testing.T) {
	s := &SigV4{AccessKey: "a", SecretKey: "b", Region: "us-east-1", Service: "s3"}
	_, err := s.PresignGetObject("not-a-url", "bucket", "key", time.Minute)
	if err == nil {
		t.Fatal("want error for non-http endpoint, got nil")
	}
}
