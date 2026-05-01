package keyservice

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/clearcompass-ai/ortholog-sdk/lifecycle/artifact"
)

// vaultBinary is the path to the Vault binary the test will exec to
// stand up a dev-mode server. Override with VAULT_BIN if your binary
// lives elsewhere; defaults to /tmp/vault (where the project's CI
// places it via `make setup-vault`).
func vaultBinary() string {
	if p := os.Getenv("VAULT_BIN"); p != "" {
		return p
	}
	return "/tmp/vault"
}

// freePort grabs a TCP port the OS guarantees is free at this instant.
// The Vault subprocess binds it almost immediately after; the gap is
// short enough to avoid races in single-test runs.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// vaultDevMode boots a `vault server -dev` subprocess on a random
// port, waits for it to be ready, enables the kv-v2 backend at
// "secret/" (kv-v1 is the default in dev mode), and returns the
// endpoint URL + dev root token. The subprocess is torn down via
// t.Cleanup.
//
// We talk to the real Vault HTTP API — no mocks. If Vault changes
// behavior, the test fails loudly.
func vaultDevMode(t *testing.T) (endpoint, token string) {
	t.Helper()

	bin := vaultBinary()
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("vault binary not found at %s: %v (set VAULT_BIN to override)", bin, err)
	}

	port := freePort(t)
	endpoint = fmt.Sprintf("http://127.0.0.1:%d", port)
	token = "dev-only-root-token-" + fmt.Sprintf("%d", port)

	cmd := exec.Command(bin, "server", "-dev",
		"-dev-root-token-id="+token,
		"-dev-listen-address=127.0.0.1:"+fmt.Sprintf("%d", port),
	)
	cmd.Env = append(os.Environ(), "VAULT_LOG_LEVEL=warn")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("StderrPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("vault start: %v", err)
	}

	// Drain stdout/stderr so the subprocess doesn't block on a full
	// pipe. We don't currently inspect them; a verbose debug build
	// could log them via t.Log.
	var drainWG sync.WaitGroup
	drainWG.Add(2)
	go func() { defer drainWG.Done(); io.Copy(io.Discard, stdout) }()
	go func() {
		defer drainWG.Done()
		s := bufio.NewScanner(stderr)
		for s.Scan() {
			// t.Log("vault: " + s.Text()) // uncomment for debug
		}
	}()

	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		drainWG.Wait()
	})

	// Wait for Vault to report unsealed.
	deadline := time.Now().Add(15 * time.Second)
	hc := &http.Client{Timeout: 500 * time.Millisecond}
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, endpoint+"/v1/sys/health", nil)
		req.Header.Set("X-Vault-Token", token)
		resp, err := hc.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				goto ready
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatal("vault dev mode did not become ready within 15s")
ready:

	// Dev mode mounts kv-v1 at "secret/". Disable + re-mount as kv-v2.
	if err := vaultRequest(hc, http.MethodDelete, endpoint+"/v1/sys/mounts/secret", token, nil); err != nil {
		t.Fatalf("disable kv-v1: %v", err)
	}
	if err := vaultRequest(hc, http.MethodPost, endpoint+"/v1/sys/mounts/secret", token,
		map[string]any{"type": "kv", "options": map[string]string{"version": "2"}}); err != nil {
		t.Fatalf("mount kv-v2: %v", err)
	}
	// Dev mode does NOT auto-mount transit. Mount unconditionally.
	if err := vaultRequest(hc, http.MethodPost, endpoint+"/v1/sys/mounts/transit", token,
		map[string]any{"type": "transit"}); err != nil {
		t.Fatalf("mount transit: %v", err)
	}

	return endpoint, token
}

// vaultRequest is a tiny helper for the test setup path that reuses
// the production HTTP code's content-type/auth conventions.
func vaultRequest(hc *http.Client, method, url, token string, body any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := jsonMarshal(body)
		if err != nil {
			return err
		}
		rdr = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("X-Vault-Token", token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("vault %s %s: HTTP %d: %s",
			method, url, resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}
	return nil
}

// jsonMarshal is a tiny indirection so we don't pull encoding/json
// into the test header again — keeps imports tidy.
func jsonMarshal(v any) ([]byte, error) {
	type marshaler interface {
		MarshalJSON() ([]byte, error)
	}
	if m, ok := v.(marshaler); ok {
		return m.MarshalJSON()
	}
	return jsonStdMarshal(v)
}

// TestVaultTransit_Conformance runs the SDK's shared conformance suite
// against a real Vault Transit + kv-v2 backend (dev mode).
func TestVaultTransit_Conformance(t *testing.T) {
	endpoint, token := vaultDevMode(t)
	svc, err := NewVaultTransit(VaultTransitConfig{
		Endpoint: endpoint,
		Token:    token,
	})
	if err != nil {
		t.Fatalf("NewVaultTransit: %v", err)
	}
	artifact.RunConformance(t, svc)
}

// TestVaultTransit_TrustClass pins the trust-class declaration. Vault
// Transit OSS keeps the KEK inside Vault's storage backend (HSM-
// protected if Vault is configured with auto-unseal against an HSM),
// but the DEK appears in process memory briefly during operations.
// That is the ClassEnvelope contract.
func TestVaultTransit_TrustClass(t *testing.T) {
	svc, err := NewVaultTransit(VaultTransitConfig{
		Endpoint: "http://127.0.0.1:0", Token: "x",
	})
	if err != nil {
		t.Fatalf("NewVaultTransit: %v", err)
	}
	if svc.TrustClass() != artifact.ClassEnvelope {
		t.Errorf("TrustClass = %v, want ClassEnvelope", svc.TrustClass())
	}
}

// TestNewVaultTransit_RejectsMissingEndpoint locks the constructor's
// validation contract.
func TestNewVaultTransit_RejectsMissingEndpoint(t *testing.T) {
	_, err := NewVaultTransit(VaultTransitConfig{Token: "x"})
	if err == nil {
		t.Fatal("expected error for missing Endpoint, got nil")
	}
}

// TestNewVaultTransit_RejectsMissingToken locks the constructor's
// validation contract.
func TestNewVaultTransit_RejectsMissingToken(t *testing.T) {
	_, err := NewVaultTransit(VaultTransitConfig{Endpoint: "http://x"})
	if err == nil {
		t.Fatal("expected error for missing Token, got nil")
	}
}

// kvWriteFailRT is a test-only http.RoundTripper that fails kv-v2
// data writes (POST /v1/secret/data/...) and passes everything else
// through. Used to simulate a partial Vault outage where Transit is
// healthy but kv-v2 is broken (mount permission, storage backend
// full, etc.) — the leaky scenario that motivates the post-encrypt
// Transit-key rollback in GenerateAndEncrypt.
type kvWriteFailRT struct{ inner http.RoundTripper }

func (rt *kvWriteFailRT) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/secret/data/") {
		return nil, errors.New("simulated kv-v2 outage")
	}
	return rt.inner.RoundTrip(r)
}

// listTransitKeys queries Vault for all transit-key names at the
// default mount. Returns nil for an empty namespace (Vault returns
// HTTP 404 in that case). Used to verify rollback removed orphans.
func listTransitKeys(t *testing.T, hc *http.Client, endpoint, token string) []string {
	t.Helper()
	req, err := http.NewRequest("LIST", endpoint+"/v1/transit/keys", nil)
	if err != nil {
		t.Fatalf("new LIST request: %v", err)
	}
	req.Header.Set("X-Vault-Token", token)
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("LIST transit/keys: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		t.Fatalf("LIST transit/keys HTTP %d: %s", resp.StatusCode, body)
	}
	var parsed struct {
		Data struct {
			Keys []string `json:"keys"`
		} `json:"data"`
	}
	if err := jsonStdUnmarshalReader(resp.Body, &parsed); err != nil {
		t.Fatalf("decode LIST: %v", err)
	}
	return parsed.Data.Keys
}

// TestVaultTransit_RollbackTransitKey_OnKVFailure pins the post-
// encrypt rollback contract: when kv-v2 write fails, the per-
// artifact Transit key created moments earlier is deleted before
// returning. Without this, every retry under a sustained kv-v2
// outage leaks a Transit key (the caller has zero CID, so they
// can't clean it up).
func TestVaultTransit_RollbackTransitKey_OnKVFailure(t *testing.T) {
	endpoint, token := vaultDevMode(t)

	plain := &http.Client{}
	if got := listTransitKeys(t, plain, endpoint, token); len(got) != 0 {
		t.Fatalf("pre-test transit keys not empty: %v", got)
	}

	failing := &http.Client{Transport: &kvWriteFailRT{inner: http.DefaultTransport}}
	svc, err := NewVaultTransit(VaultTransitConfig{
		Endpoint:   endpoint,
		Token:      token,
		HTTPClient: failing,
	})
	if err != nil {
		t.Fatalf("NewVaultTransit: %v", err)
	}
	_, _, err = svc.GenerateAndEncrypt(context.Background(), []byte("hello"))
	if err == nil {
		t.Fatal("expected GenerateAndEncrypt to fail under injected kv-v2 outage, got nil")
	}

	if got := listTransitKeys(t, plain, endpoint, token); len(got) != 0 {
		t.Fatalf("expected zero transit keys after rolled-back attempt, got %d: %v", len(got), got)
	}

	// Sanity: a healthy follow-up call still works (the failure
	// mode left no stale state behind).
	healthy, err := NewVaultTransit(VaultTransitConfig{Endpoint: endpoint, Token: token})
	if err != nil {
		t.Fatalf("NewVaultTransit (healthy): %v", err)
	}
	if _, _, err := healthy.GenerateAndEncrypt(context.Background(), []byte("hi")); err != nil {
		t.Fatalf("post-rollback GenerateAndEncrypt failed: %v", err)
	}
}

// TestVaultTransit_ServiceUnavailable_OnUnreachable asserts that a
// dead endpoint surfaces as artifact.ErrServiceUnavailable so callers
// can errors.Is against the typed sentinel.
func TestVaultTransit_ServiceUnavailable_OnUnreachable(t *testing.T) {
	port := freePort(t)
	svc, err := NewVaultTransit(VaultTransitConfig{
		Endpoint:   fmt.Sprintf("http://127.0.0.1:%d", port),
		Token:      "x",
		HTTPClient: &http.Client{Timeout: 200 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("NewVaultTransit: %v", err)
	}
	_, _, err = svc.GenerateAndEncrypt(context.Background(), []byte("x"))
	if !errors.Is(err, artifact.ErrServiceUnavailable) {
		t.Fatalf("want ErrServiceUnavailable, got %v", err)
	}
}
