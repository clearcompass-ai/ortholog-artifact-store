/*
backends/ipfs.go — IPFS backend via Kubo RPC API.

All Kubo calls go through doRPC() which adds a single conditional
Authorization: Bearer header. This is the only provider abstraction
needed for Filebase, Pinata, or any authenticated IPFS cluster.

Push forces CIDv1 + raw leaves + SHA-256 to ensure the IPFS-computed
CID uses the same hash algorithm as the SDK. Verification compares
at the digest level (32-byte SHA-256), not the full CID encoding,
because IPFS CIDs include multibase + multicodec prefixes that the
SDK's storage.CID does not.

Algorithm support: SHA-256 only.

  IPFS's content-addressing is multihash-coded, and this backend pins
  multihash tag 0x12 (SHA-256) for strict round-trip equality with the
  SDK's storage.CID. Any algorithm registered through
  storage.RegisterAlgorithm that is NOT AlgoSHA256 is rejected at the
  call boundary with storage.ErrNotSupported. Failing closed is
  required: silently stripping the algorithm tag and pinning under a
  SHA-256 CIDv1 would produce a stored CID whose Bytes() disagrees
  with what the SDK signed (ADR-005 §2 wire-form mandate), and that
  disagreement would propagate into PRE Grant SplitID derivations
  (crypto/artifact/split_id.go:42-53) — recipients would compute the
  wrong SplitID and look up the wrong commitment.

Delete returns storage.ErrNotSupported (IPFS has best-effort GC, not
guaranteed deletion). Cryptographic erasure in IPFS means destroying
the key — the ciphertext may persist on the network.
*/
package backends

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// IPFSConfig holds IPFS backend settings.
type IPFSConfig struct {
	APIEndpoint string
	Gateway     string
	BearerToken string
}

// IPFSBackend implements BackendProvider for IPFS via Kubo RPC.
type IPFSBackend struct {
	cfg    IPFSConfig
	client *http.Client
}

// NewIPFSBackend creates an IPFS backend.
func NewIPFSBackend(cfg IPFSConfig) *IPFSBackend {
	return &IPFSBackend{cfg: cfg, client: &http.Client{Timeout: 120 * time.Second}}
}

// ensureIPFSAlgorithm is the algorithm guard for every IPFSBackend
// operation that touches a CID. IPFS content-addressing is multihash-
// coded and this backend pins multihash tag 0x12 (SHA-256). Any other
// algorithm registered via storage.RegisterAlgorithm cannot be honored
// without transcoding, so we return storage.ErrNotSupported wrapped
// with the operation name and the offending algorithm tag. See the
// package godoc for the wire-form rationale (ADR-005 §2).
func ensureIPFSAlgorithm(cid storage.CID, op string) error {
	if cid.Algorithm == storage.AlgoSHA256 {
		return nil
	}
	return fmt.Errorf("ipfs/%s: %w (CID algorithm tag 0x%02x; IPFS backend supports SHA-256 only)",
		op, storage.ErrNotSupported, byte(cid.Algorithm))
}

// doRPC executes a Kubo RPC call. All IPFS operations go through here.
// Adds Authorization: Bearer header if BearerToken is set.
func (b *IPFSBackend) doRPC(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	url := b.cfg.APIEndpoint + path
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if b.cfg.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+b.cfg.BearerToken)
	}
	return b.client.Do(req)
}

func (b *IPFSBackend) Push(cid storage.CID, data []byte) error {
	if err := ensureIPFSAlgorithm(cid, "push"); err != nil {
		return err
	}
	ctx := context.Background()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", "artifact")
	if err != nil {
		return fmt.Errorf("ipfs/push: create form: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return fmt.Errorf("ipfs/push: write form: %w", err)
	}
	w.Close()

	path := "/api/v0/add?cid-version=1&raw-leaves=true&hash=sha2-256&pin=true"
	resp, err := b.doRPC(ctx, "POST", path, &buf, w.FormDataContentType())
	if err != nil {
		return fmt.Errorf("ipfs/push: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("ipfs/push: HTTP %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Hash string `json:"Hash"`
		Size string `json:"Size"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("ipfs/push: decode response: %w", err)
	}

	ipfsDigest, err := ExtractDigestFromIPFSCID(result.Hash)
	if err != nil {
		return fmt.Errorf("ipfs/push: extract digest: %w", err)
	}
	if !bytes.Equal(ipfsDigest, cid.Digest) {
		return fmt.Errorf("ipfs/push: CID digest mismatch: IPFS=%x SDK=%x (data corruption in transit)",
			ipfsDigest, cid.Digest)
	}

	return nil
}

func (b *IPFSBackend) Fetch(cid storage.CID) ([]byte, error) {
	if err := ensureIPFSAlgorithm(cid, "fetch"); err != nil {
		return nil, err
	}
	ctx := context.Background()
	ipfsCID := SDKCIDToIPFSPath(cid)
	path := fmt.Sprintf("/api/v0/cat?arg=%s", ipfsCID)
	resp, err := b.doRPC(ctx, "POST", path, nil, "")
	if err != nil {
		return nil, fmt.Errorf("ipfs/fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusInternalServerError {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if strings.Contains(string(body), "not found") || strings.Contains(string(body), "block was not found") {
			return nil, storage.ErrContentNotFound
		}
		return nil, fmt.Errorf("ipfs/fetch: %s", body)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("ipfs/fetch: HTTP %d: %s", resp.StatusCode, body)
	}
	return io.ReadAll(resp.Body)
}

func (b *IPFSBackend) Exists(cid storage.CID) (bool, error) {
	if err := ensureIPFSAlgorithm(cid, "exists"); err != nil {
		return false, err
	}
	ctx := context.Background()
	ipfsCID := SDKCIDToIPFSPath(cid)
	path := fmt.Sprintf("/api/v0/pin/ls?arg=%s&type=all", ipfsCID)
	resp, err := b.doRPC(ctx, "POST", path, nil, "")
	if err != nil {
		return false, fmt.Errorf("ipfs/exists: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK, nil
}

func (b *IPFSBackend) Pin(cid storage.CID) error {
	if err := ensureIPFSAlgorithm(cid, "pin"); err != nil {
		return err
	}
	ctx := context.Background()
	ipfsCID := SDKCIDToIPFSPath(cid)
	path := fmt.Sprintf("/api/v0/pin/add?arg=%s", ipfsCID)
	resp, err := b.doRPC(ctx, "POST", path, nil, "")
	if err != nil {
		return fmt.Errorf("ipfs/pin: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ipfs/pin: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (b *IPFSBackend) Delete(_ storage.CID) error {
	return storage.ErrNotSupported
}

func (b *IPFSBackend) Resolve(cid storage.CID, _ time.Duration) (*storage.RetrievalCredential, error) {
	if err := ensureIPFSAlgorithm(cid, "resolve"); err != nil {
		return nil, err
	}
	ipfsCID := SDKCIDToIPFSPath(cid)
	url := fmt.Sprintf("%s/ipfs/%s", b.cfg.Gateway, ipfsCID)
	return &storage.RetrievalCredential{
		Method: storage.MethodIPFS,
		URL:    url,
		Expiry: nil, // IPFS: public, no expiry
	}, nil
}

func (b *IPFSBackend) Healthy() error {
	ctx := context.Background()
	resp, err := b.doRPC(ctx, "POST", "/api/v0/id", nil, "")
	if err != nil {
		return fmt.Errorf("ipfs/health: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ipfs/health: HTTP %d", resp.StatusCode)
	}
	return nil
}

// ExtractDigestFromIPFSCID parses an IPFS CIDv1 string and returns the
// raw SHA-256 digest (32 bytes).
func ExtractDigestFromIPFSCID(cidStr string) ([]byte, error) {
	if len(cidStr) < 2 {
		return nil, fmt.Errorf("CID too short: %q", cidStr)
	}

	var raw []byte
	var err error
	switch cidStr[0] {
	case 'b':
		raw, err = decodeBase32Lower(cidStr[1:])
	case 'f':
		raw, err = hex.DecodeString(cidStr[1:])
	default:
		return nil, fmt.Errorf("unsupported multibase prefix %q in CID %q", cidStr[0:1], cidStr)
	}
	if err != nil {
		return nil, fmt.Errorf("decode CID %q: %w", cidStr, err)
	}

	if len(raw) < 36 {
		return nil, fmt.Errorf("CID raw bytes too short (%d)", len(raw))
	}

	pos := 0
	for pos < len(raw) && raw[pos]&0x80 != 0 {
		pos++
	}
	pos++
	for pos < len(raw) && raw[pos]&0x80 != 0 {
		pos++
	}
	pos++

	if pos+2+32 > len(raw) {
		return nil, fmt.Errorf("CID too short for multihash at offset %d", pos)
	}
	if raw[pos] != 0x12 {
		return nil, fmt.Errorf("expected SHA-256 multihash tag 0x12, got 0x%02x", raw[pos])
	}
	if raw[pos+1] != 0x20 {
		return nil, fmt.Errorf("expected digest length 32 (0x20), got 0x%02x", raw[pos+1])
	}
	return raw[pos+2 : pos+2+32], nil
}

// SDKCIDToIPFSPath converts an SDK CID to an IPFS CID string.
func SDKCIDToIPFSPath(cid storage.CID) string {
	raw := make([]byte, 0, 2+2+32)
	raw = append(raw, 0x01)
	raw = append(raw, 0x55)
	raw = append(raw, 0x12, 0x20)
	raw = append(raw, cid.Digest...)
	return "b" + encodeBase32Lower(raw)
}

const base32Alphabet = "abcdefghijklmnopqrstuvwxyz234567"

func encodeBase32Lower(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.Grow((len(data)*8 + 4) / 5)
	buffer := uint64(0)
	bits := 0
	for _, b := range data {
		buffer = (buffer << 8) | uint64(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			sb.WriteByte(base32Alphabet[(buffer>>uint(bits))&0x1f])
		}
	}
	if bits > 0 {
		sb.WriteByte(base32Alphabet[(buffer<<uint(5-bits))&0x1f])
	}
	return sb.String()
}

func decodeBase32Lower(s string) ([]byte, error) {
	var lookup [256]byte
	for i := range lookup {
		lookup[i] = 0xFF
	}
	for i, c := range base32Alphabet {
		lookup[c] = byte(i)
	}

	result := make([]byte, 0, len(s)*5/8)
	buffer := uint64(0)
	bits := 0
	for _, c := range []byte(s) {
		if c == '=' {
			break
		}
		val := lookup[c]
		if val == 0xFF {
			return nil, fmt.Errorf("invalid base32 character %q", c)
		}
		buffer = (buffer << 5) | uint64(val)
		bits += 5
		if bits >= 8 {
			bits -= 8
			result = append(result, byte(buffer>>uint(bits)))
		}
	}
	return result, nil
}
