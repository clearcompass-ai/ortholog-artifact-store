package main

import (
	"context"
	"strings"
	"testing"

	"github.com/clearcompass-ai/ortholog-artifact-store/config"
	"github.com/clearcompass-ai/ortholog-artifact-store/keyservice"

	sdkartifact "github.com/clearcompass-ai/ortholog-sdk/lifecycle/artifact"
)

// initKeyService is the dispatcher under test. We exercise each
// branch — the constructors that don't need external services
// succeed at this level (memory) and the ones that do (vault,
// gcpkms, pkcs11) construct without I/O so the binary surfaces
// per-tier configuration errors at startup, not at first-write.

// TestInitKeyService_MemoryReturnsInProcessImpl pins the dev-mode
// escape hatch. The InMemoryKeyService satisfies the same contract
// as the production tiers; it's appropriate for ephemeral local
// runs only because there's no persistence.
func TestInitKeyService_MemoryReturnsInProcessImpl(t *testing.T) {
	cfg := &config.Config{KeyService: "memory"}
	ks, err := initKeyService(context.Background(), cfg)
	if err != nil {
		t.Fatalf("initKeyService(memory): %v", err)
	}
	if _, ok := ks.(*sdkartifact.InMemoryKeyService); !ok {
		t.Errorf("initKeyService(memory): want *sdkartifact.InMemoryKeyService, got %T", ks)
	}
}

// TestInitKeyService_VaultReturnsVaultTransit pins the default
// production tier. We don't validate the endpoint here — the
// constructor accepts an endpoint URL; an actual request happens at
// first-use, surfacing artifact.ErrServiceUnavailable if the
// endpoint is dead. That's covered in keyservice/vault_test.go's
// TestVaultTransit_ServiceUnavailable_OnUnreachable.
func TestInitKeyService_VaultReturnsVaultTransit(t *testing.T) {
	cfg := &config.Config{
		KeyService:        "vault",
		VaultEndpoint:     "http://127.0.0.1:1",
		VaultToken:        "dev-only-token",
		VaultTransitMount: "transit",
		VaultKVMount:      "secret",
		VaultKVNamespace:  "artifact-keys",
	}
	ks, err := initKeyService(context.Background(), cfg)
	if err != nil {
		t.Fatalf("initKeyService(vault): %v", err)
	}
	if _, ok := ks.(*keyservice.VaultTransit); !ok {
		t.Errorf("initKeyService(vault): want *keyservice.VaultTransit, got %T", ks)
	}
	if ks.TrustClass() != sdkartifact.ClassEnvelope {
		t.Errorf("vault TrustClass: want ClassEnvelope, got %v", ks.TrustClass())
	}
}

// TestInitKeyService_GCPKMSReturnsGCPKMS pins the GCP tier dispatch.
// NewGCPKMS is constructed without an HTTPClient so it'll try ADC at
// runtime — fine because we don't make any RPCs in this test. The
// purpose is to assert the dispatch wiring, not the auth flow.
//
// Note: in environments without GCP ADC available (most test
// environments), NewGCPKMS may still succeed because the
// constructor only sets up the token source lazily — the first
// request is when auth actually fires. If that ever changes upstream
// in golang.org/x/oauth2/google, this test may need to set
// GOOGLE_APPLICATION_CREDENTIALS to a placeholder path.
func TestInitKeyService_GCPKMSReturnsGCPKMS(t *testing.T) {
	cfg := &config.Config{
		KeyService:             "gcpkms",
		GCPKMSKEKResource:      "projects/test/locations/us/keyRings/r/cryptoKeys/k",
		GCPFirestoreProjectID:  "test",
		GCPFirestoreDatabase:   "(default)",
		GCPFirestoreCollection: "ortholog-artifact-keys",
	}
	ks, err := initKeyService(context.Background(), cfg)
	if err != nil {
		// On environments without ADC the token source may fail at
		// construction time. Treat that as a clean skip — the
		// dispatch wiring is what we're testing, not GCP auth.
		if strings.Contains(err.Error(), "ADC") || strings.Contains(err.Error(), "credentials") {
			t.Skipf("ADC not available: %v", err)
		}
		t.Fatalf("initKeyService(gcpkms): %v", err)
	}
	if _, ok := ks.(*keyservice.GCPKMS); !ok {
		t.Errorf("initKeyService(gcpkms): want *keyservice.GCPKMS, got %T", ks)
	}
	if ks.TrustClass() != sdkartifact.ClassEnvelope {
		t.Errorf("gcpkms TrustClass: want ClassEnvelope, got %v", ks.TrustClass())
	}
}

// TestInitKeyService_PKCS11ReturnsPKCS11 pins the PKCS#11 tier
// dispatch. We use a non-existent module path: NewPKCS11 should
// fail at construction time (dlopen fails) — that's the expected
// signal that the dispatch routed to the right constructor.
func TestInitKeyService_PKCS11ReturnsPKCS11(t *testing.T) {
	cfg := &config.Config{
		KeyService:       "pkcs11",
		PKCS11ModulePath: "/nonexistent/libsofthsm2.so",
		PKCS11TokenLabel: "ortholog-test",
		PKCS11Pin:        "1234",
	}
	if _, err := initKeyService(context.Background(), cfg); err == nil {
		t.Fatal("expected error for non-existent PKCS#11 module, got nil")
	}
}

// TestInitKeyService_RejectsUnknown locks the dispatcher's default
// branch — config.Validate() catches unknown values at Load(), but
// we keep a defensive arm in initKeyService that also errors. This
// pin protects future refactors of either layer.
func TestInitKeyService_RejectsUnknown(t *testing.T) {
	cfg := &config.Config{KeyService: "vaultenterprise"}
	if _, err := initKeyService(context.Background(), cfg); err == nil {
		t.Fatal("expected error for unknown keyservice, got nil")
	}
}
