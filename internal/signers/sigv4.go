/*
Package signers provides request-signing helpers shared across the
Wave 2 (integration) and Wave 3 (staging) test packages.

Production code does NOT depend on this package. The production S3
backend takes an injected RequestSigner interface (backends/s3.go);
integration and staging tests construct one using the SigV4 type here.

Kept under internal/ so external consumers cannot depend on it —
this isn't a general-purpose AWS signer, just enough SigV4 to make
S3-compatible test setups work.
*/
package signers

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// SigV4 signs HTTP requests using AWS Signature Version 4.
// SigV4 is the protocol-level signing algorithm; the supported S3-
// protocol implementation in this artifact store is RustFS.
type SigV4 struct {
	AccessKey string
	SecretKey string
	Region    string
	Service   string // typically "s3"
}

// SignRequest satisfies backends.S3RequestSigner. Integration and staging
// tests construct a SigV4 and pass it directly to S3Config.RequestSigner.
func (s *SigV4) SignRequest(req *http.Request) error { return s.signAt(req, time.Now().UTC()) }

// signAt is the underlying implementation. Extracted so the presigner
// (below) can share clock handling without invoking request signing twice.
func (s *SigV4) signAt(req *http.Request, now time.Time) error {
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	var bodySha string
	if req.Body != nil && req.Body != http.NoBody {
		buf, err := io.ReadAll(req.Body)
		if err != nil {
			return fmt.Errorf("read body: %w", err)
		}
		sum := sha256.Sum256(buf)
		bodySha = hex.EncodeToString(sum[:])
		req.Body = io.NopCloser(bytes.NewReader(buf))
		req.ContentLength = int64(len(buf))
	} else {
		sum := sha256.Sum256(nil)
		bodySha = hex.EncodeToString(sum[:])
	}
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", bodySha)
	if req.Host != "" {
		req.Header.Set("Host", req.Host)
	} else {
		req.Header.Set("Host", req.URL.Host)
	}

	signed, canonHeaders := canonicalizeHeaders(req)
	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	creq := strings.Join([]string{
		req.Method,
		path,
		req.URL.RawQuery,
		canonHeaders,
		signed,
		bodySha,
	}, "\n")

	scope := fmt.Sprintf("%s/%s/%s/aws4_request", dateStamp, s.Region, s.Service)
	creqHash := sha256.Sum256([]byte(creq))
	sts := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hex.EncodeToString(creqHash[:]),
	}, "\n")

	kDate := hmacSHA256([]byte("AWS4"+s.SecretKey), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(s.Region))
	kService := hmacSHA256(kRegion, []byte(s.Service))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	sig := hex.EncodeToString(hmacSHA256(kSigning, []byte(sts)))

	auth := fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		s.AccessKey, scope, signed, sig,
	)
	req.Header.Set("Authorization", auth)
	return nil
}

// PresignGetObject returns a query-signed URL valid for the given
// expiry. Satisfies backends.S3URLSigner.
//
// Path-style vs virtual-host-style: the caller passes an endpoint
// that reflects the deployment. This presigner constructs the URL
// in the same style.
func (s *SigV4) PresignGetObject(endpoint, bucket, key string, expiry time.Duration) (string, error) {
	// Scheme + host from endpoint (e.g. https://s3.us-east-1.amazonaws.com)
	if !strings.HasPrefix(endpoint, "http") {
		return "", fmt.Errorf("endpoint must start with http(s)://: %q", endpoint)
	}
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	scope := fmt.Sprintf("%s/%s/%s/aws4_request", dateStamp, s.Region, s.Service)

	host := strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://")
	scheme := "https"
	if strings.HasPrefix(endpoint, "http://") {
		scheme = "http"
	}

	// Path-style URL: /<bucket>/<key>
	path := "/" + bucket + "/" + key

	// Canonical query (URL-encoded, sorted).
	q := url.Values{}
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", s.AccessKey+"/"+scope)
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", fmt.Sprintf("%d", int(expiry.Seconds())))
	q.Set("X-Amz-SignedHeaders", "host")
	canonicalQuery := q.Encode()

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
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hex.EncodeToString(creqHash[:]),
	}, "\n")

	kDate := hmacSHA256([]byte("AWS4"+s.SecretKey), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(s.Region))
	kService := hmacSHA256(kRegion, []byte(s.Service))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	sig := hex.EncodeToString(hmacSHA256(kSigning, []byte(sts)))

	return fmt.Sprintf("%s://%s%s?%s&X-Amz-Signature=%s",
		scheme, host, path, canonicalQuery, sig), nil
}

func hmacSHA256(key, msg []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return h.Sum(nil)
}

func canonicalizeHeaders(req *http.Request) (string, string) {
	keys := make([]string, 0, len(req.Header)+1)
	for k := range req.Header {
		keys = append(keys, strings.ToLower(k))
	}
	if _, ok := req.Header["Host"]; !ok {
		keys = append(keys, "host")
	}
	sort.Strings(keys)

	var canon strings.Builder
	for _, k := range keys {
		var v string
		if k == "host" {
			v = req.Header.Get("Host")
			if v == "" {
				v = req.URL.Host
			}
		} else {
			v = strings.TrimSpace(req.Header.Get(k))
		}
		canon.WriteString(k)
		canon.WriteByte(':')
		canon.WriteString(v)
		canon.WriteByte('\n')
	}
	signed := strings.Join(keys, ";")
	return signed, canon.String()
}
