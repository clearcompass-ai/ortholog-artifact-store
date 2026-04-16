package signers

import (
	"strings"
	"testing"
	"time"
)

func TestBoundS3Presigner_ForwardsToSigner(t *testing.T) {
	bp := &BoundS3Presigner{
		Signer: &SigV4{
			AccessKey: "AKIA",
			SecretKey: "secret",
			Region:    "us-east-1",
			Service:   "s3",
		},
		Endpoint: "https://s3.us-east-1.amazonaws.com",
	}
	url, err := bp.PresignGetObject("bucket-a", "key-z", time.Minute)
	if err != nil {
		t.Fatalf("PresignGetObject: %v", err)
	}
	if !strings.Contains(url, "/bucket-a/key-z") {
		t.Errorf("URL missing bucket/key: %s", url)
	}
	if !strings.Contains(url, "X-Amz-Signature=") {
		t.Errorf("URL missing signature: %s", url)
	}
}
