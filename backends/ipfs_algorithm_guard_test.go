package backends

import (
	"crypto/sha256"
	"errors"
	"sync"
	"testing"

	"github.com/clearcompass-ai/ortholog-artifact-store/internal/testutil"
	"github.com/clearcompass-ai/ortholog-sdk/storage"
)

// ─── v7.75 IPFS algorithm guard (ADR-005 §2 wire-form mandate) ───────
//
// IPFS multihash pins SHA-256 (tag 0x12). The IPFS backend refuses any
// CID minted under a different algorithm registered through
// storage.RegisterAlgorithm — silently stripping the algorithm tag and
// pinning under a SHA-256 CIDv1 would produce a stored object whose
// CID.Bytes() disagrees with what the SDK signed, and that disagreement
// propagates into the PRE Grant SplitID derivation. The guard fails
// closed at the call boundary on Push / Fetch / Exists / Pin / Resolve.
//
// These tests are Wave 1 (no Docker) so the regression catches at every
// `go test ./...` run, not only in the nightly Wave 2 integration job.

const (
	// guardTestAlgoTag picks a tag distinct from every other test
	// fixture in this binary (api package uses 0xC1; tests/conformance
	// uses 0xC2; SDK fixtures use 0xE2 / 0xF1) so the global registry
	// stays unambiguous across packages.
	guardTestAlgoTag    storage.HashAlgorithm = 0xC3
	guardTestAlgoName                         = "test-algo-ipfs-guard"
	guardTestDigestSize                       = 16
)

var registerGuardTestAlgorithmOnce sync.Once

func registerGuardTestAlgorithm(t *testing.T) {
	t.Helper()
	registerGuardTestAlgorithmOnce.Do(func() {
		storage.RegisterAlgorithm(
			guardTestAlgoTag,
			guardTestAlgoName,
			guardTestDigestSize,
			func(data []byte) []byte {
				h := sha256.Sum256(data)
				return h[:guardTestDigestSize]
			},
		)
	})
}

func TestIPFS_AlgorithmGuard_PushRejectsNonSHA256(t *testing.T) {
	registerGuardTestAlgorithm(t)
	fake := testutil.NewKuboFake(t)
	b := newIPFSBackendWithFake(t, fake, "")

	cid := storage.ComputeWith([]byte("payload"), guardTestAlgoTag)
	err := b.Push(cid, []byte("payload"))
	if !errors.Is(err, storage.ErrNotSupported) {
		t.Fatalf("Push: want errors.Is(err, ErrNotSupported), got %v", err)
	}

	// And the backend MUST NOT have issued any HTTP call to the fake —
	// the guard must fail closed before any wire activity.
	if reqs := fake.Requests(); len(reqs) != 0 {
		t.Fatalf("Push under non-SHA-256 algorithm sent %d requests; want 0 (guard must fail closed before HTTP)",
			len(reqs))
	}
}

func TestIPFS_AlgorithmGuard_FetchRejectsNonSHA256(t *testing.T) {
	registerGuardTestAlgorithm(t)
	fake := testutil.NewKuboFake(t)
	b := newIPFSBackendWithFake(t, fake, "")

	cid := storage.ComputeWith([]byte("payload"), guardTestAlgoTag)
	_, err := b.Fetch(cid)
	if !errors.Is(err, storage.ErrNotSupported) {
		t.Fatalf("Fetch: want ErrNotSupported, got %v", err)
	}
	if reqs := fake.Requests(); len(reqs) != 0 {
		t.Fatalf("Fetch sent %d requests; want 0 (guard must fail closed before HTTP)",
			len(reqs))
	}
}

func TestIPFS_AlgorithmGuard_ExistsRejectsNonSHA256(t *testing.T) {
	registerGuardTestAlgorithm(t)
	fake := testutil.NewKuboFake(t)
	b := newIPFSBackendWithFake(t, fake, "")

	cid := storage.ComputeWith([]byte("payload"), guardTestAlgoTag)
	exists, err := b.Exists(cid)
	if !errors.Is(err, storage.ErrNotSupported) {
		t.Fatalf("Exists: want ErrNotSupported, got %v", err)
	}
	if exists {
		t.Fatal("Exists: want false on rejection, got true")
	}
}

func TestIPFS_AlgorithmGuard_PinRejectsNonSHA256(t *testing.T) {
	registerGuardTestAlgorithm(t)
	fake := testutil.NewKuboFake(t)
	b := newIPFSBackendWithFake(t, fake, "")

	cid := storage.ComputeWith([]byte("payload"), guardTestAlgoTag)
	err := b.Pin(cid)
	if !errors.Is(err, storage.ErrNotSupported) {
		t.Fatalf("Pin: want ErrNotSupported, got %v", err)
	}
}

func TestIPFS_AlgorithmGuard_ResolveRejectsNonSHA256(t *testing.T) {
	// Resolve doesn't touch the network at all (it constructs the
	// gateway URL via SDKCIDToIPFSPath), but stripping the algorithm
	// tag during URL construction would silently emit a wrong CID
	// string. The guard rejects before SDKCIDToIPFSPath runs.
	registerGuardTestAlgorithm(t)
	fake := testutil.NewKuboFake(t)
	b := newIPFSBackendWithFake(t, fake, "")

	cid := storage.ComputeWith([]byte("payload"), guardTestAlgoTag)
	cred, err := b.Resolve(cid, 0)
	if !errors.Is(err, storage.ErrNotSupported) {
		t.Fatalf("Resolve: want ErrNotSupported, got %v", err)
	}
	if cred != nil {
		t.Fatalf("Resolve: want nil credential on rejection, got %+v", cred)
	}
}

func TestIPFS_AlgorithmGuard_ErrorMessageNamesAlgorithmAndOperation(t *testing.T) {
	// SIEM / log-pipeline filtering relies on the rejection error
	// carrying both the operation name (push / fetch / …) and the
	// algorithm tag. This test pins both substrings so a refactor of
	// the message format can't silently break operator alerting.
	registerGuardTestAlgorithm(t)
	fake := testutil.NewKuboFake(t)
	b := newIPFSBackendWithFake(t, fake, "")

	cid := storage.ComputeWith([]byte("payload"), guardTestAlgoTag)
	err := b.Push(cid, []byte("payload"))
	if err == nil {
		t.Fatal("Push: want error, got nil")
	}
	msg := err.Error()
	if !contains(msg, "ipfs/push") {
		t.Errorf("error message missing operation tag 'ipfs/push': %q", msg)
	}
	if !contains(msg, "0xc3") {
		t.Errorf("error message missing offending algorithm tag '0xc3': %q", msg)
	}
}

// contains is the local equivalent of strings.Contains, kept inline
// to avoid pulling another import for one call site.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
