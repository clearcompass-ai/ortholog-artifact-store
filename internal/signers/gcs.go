/*
internal/signers/gcs.go — GCS V4 signed URL generator.

Loads a service-account JSON key file and produces V4 query-signed URLs.
Satisfies backends.GCSURLSigner.

The V4 signing protocol is documented at
https://cloud.google.com/storage/docs/access-control/signed-urls-v4.
This implementation follows that spec for GET requests only — the
artifact store never needs signed PUT/DELETE URLs (Push writes go
through the backend's authenticated client).

Only staging/production uses this. Tests at Wave 1 and Wave 2 use
trivial mock signers (see internal/testutil/ and tests/integration/).
*/
package signers

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// GCSServiceAccount is the subset of a GCS service-account JSON file
// we read. Additional fields (type, project_id, etc.) are ignored.
type GCSServiceAccount struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"` // PEM-wrapped PKCS#8 RSA key
}

// LoadGCSServiceAccount reads a service-account JSON file from path
// and returns a GCSSigner ready to produce V4 URLs.
func LoadGCSServiceAccount(path string) (*GCSSigner, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var sa GCSServiceAccount
	if err := json.Unmarshal(data, &sa); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if sa.ClientEmail == "" || sa.PrivateKey == "" {
		return nil, fmt.Errorf("%s: missing client_email or private_key", path)
	}

	block, _ := pem.Decode([]byte(sa.PrivateKey))
	if block == nil {
		return nil, fmt.Errorf("%s: private_key is not valid PEM", path)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		// Some SA files use PKCS#1 — try that fallback.
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%s: parse private_key: %w", path, err)
		}
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s: private_key is not RSA", path)
	}
	return &GCSSigner{email: sa.ClientEmail, key: rsaKey}, nil
}

// GCSSigner implements backends.GCSURLSigner against a loaded service account.
type GCSSigner struct {
	email string
	key   *rsa.PrivateKey
}

// SignURL produces a V4 signed URL for GET access to the given object.
// Satisfies backends.GCSURLSigner.
func (s *GCSSigner) SignURL(bucket, object string, expiry time.Duration) (string, error) {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	// GCS V4 uses the GOOG4 family of algorithm strings.
	const alg = "GOOG4-RSA-SHA256"
	scope := fmt.Sprintf("%s/auto/storage/goog4_request", dateStamp)

	host := "storage.googleapis.com"
	path := "/" + bucket + "/" + object

	q := url.Values{}
	q.Set("X-Goog-Algorithm", alg)
	q.Set("X-Goog-Credential", s.email+"/"+scope)
	q.Set("X-Goog-Date", amzDate)
	q.Set("X-Goog-Expires", fmt.Sprintf("%d", int(expiry.Seconds())))
	q.Set("X-Goog-SignedHeaders", "host")
	canonicalQuery := encodeCanonical(q)

	canonicalHeaders := "host:" + host + "\n"
	creq := strings.Join([]string{
		"GET",
		path,
		canonicalQuery,
		canonicalHeaders,
		"host",
		"UNSIGNED-PAYLOAD",
	}, "\n")
	creqHash := sha256.Sum256([]byte(creq))

	sts := strings.Join([]string{
		alg,
		amzDate,
		scope,
		hex.EncodeToString(creqHash[:]),
	}, "\n")

	// Sign with RSA-SHA256.
	stsHash := sha256.Sum256([]byte(sts))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, stsHash[:])
	if err != nil {
		return "", fmt.Errorf("rsa sign: %w", err)
	}

	return fmt.Sprintf("https://%s%s?%s&X-Goog-Signature=%s",
		host, path, canonicalQuery, hex.EncodeToString(sig)), nil
}

// encodeCanonical sorts query keys and URL-encodes values in the
// precise way GCS V4 expects (RFC 3986 unreserved only).
func encodeCanonical(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte('&')
		}
		sb.WriteString(url.QueryEscape(k))
		sb.WriteByte('=')
		sb.WriteString(url.QueryEscape(q.Get(k)))
	}
	return sb.String()
}
