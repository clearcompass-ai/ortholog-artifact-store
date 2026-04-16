/*
internal/signers/s3_presigner.go — backends.S3URLSigner-shaped wrapper.

The S3Backend's S3URLSigner interface is:
    PresignGetObject(bucket, key string, expiry time.Duration) (string, error)

Our SigV4 struct has an equivalent method, but it requires the endpoint
to be passed at call time. Staging tests know the endpoint at construction
time, so this wrapper binds it.
*/
package signers

import (
	"time"
)

// BoundS3Presigner wraps a SigV4 plus an endpoint into the exact shape
// backends.S3URLSigner expects.
type BoundS3Presigner struct {
	Signer   *SigV4
	Endpoint string // e.g. https://s3.us-east-1.amazonaws.com
}

// PresignGetObject satisfies backends.S3URLSigner.
func (b *BoundS3Presigner) PresignGetObject(bucket, key string, expiry time.Duration) (string, error) {
	return b.Signer.PresignGetObject(b.Endpoint, bucket, key, expiry)
}
