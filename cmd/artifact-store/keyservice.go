/*
Keyservice selection — wires the configured ArtifactKeyService
implementation. See config.Config.KeyService for the env-driven
selection contract.

Backends, in order of operator preference:

  - "vault" (default): HashiCorp Vault Transit OSS. Cheapest production-
    suitable backend; requires a running Vault server reachable at
    cfg.VaultEndpoint with a token that has the policy documented in
    keyservice/vault.go's package comment.

  - "gcpkms": GCP Cloud KMS HSM + Firestore. Talks to REAL Cloud KMS
    and REAL Firestore — no fakes are wired through this path.
    Authentication uses Application Default Credentials (ADC):
    GOOGLE_APPLICATION_CREDENTIALS env var or workload identity in
    GKE/Cloud Run. The fake servers in keyservice/gcpkms_test.go are
    test-only infrastructure for hermetic CI; the running binary
    always hits real services.

  - "pkcs11": local PKCS#11 module (SoftHSM2 or vendor HSM). Talks to
    the .so at cfg.PKCS11ModulePath; requires a token initialized
    via softhsm2-util --init-token (or the vendor equivalent).

  - "memory": in-process reference implementation. Dev/test only —
    no persistence; keys vanish on process exit.
*/
package main

import (
	"context"
	"fmt"

	"github.com/clearcompass-ai/ortholog-artifact-store/config"
	"github.com/clearcompass-ai/ortholog-artifact-store/keyservice"

	sdkartifact "github.com/clearcompass-ai/ortholog-sdk/lifecycle/artifact"
)

// initKeyService constructs the ArtifactKeyService implementation
// selected by cfg.KeyService. Returns a typed error when required
// per-tier config is missing — config.Validate() catches the obvious
// cases at startup, but the constructor surfaces remaining
// backend-specific failures (unreachable endpoint, bad credentials).
func initKeyService(ctx context.Context, cfg *config.Config) (sdkartifact.ArtifactKeyService, error) {
	switch cfg.KeyService {
	case "memory":
		return sdkartifact.NewInMemoryKeyService(), nil

	case "vault":
		svc, err := keyservice.NewVaultTransit(keyservice.VaultTransitConfig{
			Endpoint:     cfg.VaultEndpoint,
			Token:        cfg.VaultToken,
			TransitMount: cfg.VaultTransitMount,
			KVMount:      cfg.VaultKVMount,
			KVNamespace:  cfg.VaultKVNamespace,
		})
		if err != nil {
			return nil, fmt.Errorf("vault keyservice: %w", err)
		}
		return svc, nil

	case "gcpkms":
		svc, err := keyservice.NewGCPKMS(ctx, keyservice.GCPKMSConfig{
			KEKResourceName:     cfg.GCPKMSKEKResource,
			FirestoreProjectID:  cfg.GCPFirestoreProjectID,
			FirestoreDatabase:   cfg.GCPFirestoreDatabase,
			FirestoreCollection: cfg.GCPFirestoreCollection,
			// HTTPClient nil → NewGCPKMS wires Application Default
			// Credentials via golang.org/x/oauth2/google. Endpoints
			// nil → public GCP. No fake-server overrides leak from
			// tests into the runtime path.
		})
		if err != nil {
			return nil, fmt.Errorf("gcpkms keyservice: %w", err)
		}
		return svc, nil

	case "pkcs11":
		svc, err := keyservice.NewPKCS11(keyservice.PKCS11Config{
			ModulePath: cfg.PKCS11ModulePath,
			TokenLabel: cfg.PKCS11TokenLabel,
			Pin:        cfg.PKCS11Pin,
		})
		if err != nil {
			return nil, fmt.Errorf("pkcs11 keyservice: %w", err)
		}
		return svc, nil

	default:
		// config.Validate() should have caught this; defensive.
		return nil, fmt.Errorf("unknown keyservice %q", cfg.KeyService)
	}
}
