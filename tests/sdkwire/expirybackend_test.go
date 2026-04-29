package sdkwire

import (
	"time"

	"github.com/clearcompass-ai/ortholog-artifact-store/backends"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// expiryBackend wraps an InMemoryBackend but stamps a fixed Expiry
// on every Resolve. Used to exercise the SupportsExpiry branch of
// the SDK-wire round-trip without spinning a signed-URL container.
//
// Defined in a _test.go file so it doesn't bleed into production
// binaries; package sdkwire has no non-test code.
type expiryBackend struct {
	*backends.InMemoryBackend
	expiry time.Time
}

func newExpiryBackend(t time.Time) *expiryBackend {
	return &expiryBackend{
		InMemoryBackend: backends.NewInMemoryBackend(),
		expiry:          t,
	}
}

// Resolve overrides InMemoryBackend.Resolve to stamp a SignedURL
// credential with the configured Expiry. This is the production-shape
// credential (RFC3339 expiry, signed-URL Method) without needing
// real signing infra.
func (b *expiryBackend) Resolve(cid storage.CID, _ time.Duration) (*storage.RetrievalCredential, error) {
	exp := b.expiry
	return &storage.RetrievalCredential{
		Method: storage.MethodSignedURL,
		URL:    "https://signed.example.test/" + cid.String(),
		Expiry: &exp,
	}, nil
}
