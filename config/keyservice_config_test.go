package config

import "testing"

// keyserviceTestEnv is the minimum env needed to run Load() under a
// specific keyservice selection without bleeding storage-backend
// defaults into the assertion logic.
func keyserviceTestEnv(extra map[string]string) map[string]string {
	base := map[string]string{
		"ARTIFACT_BACKEND":              "memory",
		"ARTIFACT_KEYSERVICE":           "",
		"ARTIFACT_VAULT_ENDPOINT":       "",
		"ARTIFACT_VAULT_TOKEN":          "",
		"ARTIFACT_VAULT_TRANSIT_MOUNT":  "",
		"ARTIFACT_VAULT_KV_MOUNT":       "",
		"ARTIFACT_VAULT_KV_NAMESPACE":   "",
		"ARTIFACT_GCP_KMS_KEK_RESOURCE": "",
		"ARTIFACT_GCP_FIRESTORE_PROJECT_ID":  "",
		"ARTIFACT_GCP_FIRESTORE_DATABASE":    "",
		"ARTIFACT_GCP_FIRESTORE_COLLECTION":  "",
		"ARTIFACT_PKCS11_MODULE_PATH":   "",
		"ARTIFACT_PKCS11_TOKEN_LABEL":   "",
		"ARTIFACT_PKCS11_PIN":           "",
	}
	for k, v := range extra {
		base[k] = v
	}
	return base
}

// TestLoad_KeyService_DefaultIsVault pins the operator-facing
// guarantee: an unconfigured ARTIFACT_KEYSERVICE selects HashiCorp
// Vault Transit OSS, and the load fails loudly when the token is
// also unset (production is not allowed to silently fall back to
// the in-memory reference impl).
func TestLoad_KeyService_DefaultIsVault(t *testing.T) {
	setEnv(t, keyserviceTestEnv(map[string]string{
		// Provide a token so default path validates cleanly.
		"ARTIFACT_VAULT_TOKEN": "dev-only-token",
	}))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.KeyService != "vault" {
		t.Errorf("KeyService default: want vault, got %q", cfg.KeyService)
	}
	if cfg.VaultEndpoint != "http://127.0.0.1:8200" {
		t.Errorf("VaultEndpoint default: want http://127.0.0.1:8200, got %q", cfg.VaultEndpoint)
	}
	if cfg.VaultTransitMount != "transit" {
		t.Errorf("VaultTransitMount default: want transit, got %q", cfg.VaultTransitMount)
	}
	if cfg.VaultKVMount != "secret" {
		t.Errorf("VaultKVMount default: want secret, got %q", cfg.VaultKVMount)
	}
	if cfg.VaultKVNamespace != "artifact-keys" {
		t.Errorf("VaultKVNamespace default: want artifact-keys, got %q", cfg.VaultKVNamespace)
	}
}

// TestLoad_KeyService_VaultRejectsMissingToken locks the
// must-configure-token contract for the vault tier.
func TestLoad_KeyService_VaultRejectsMissingToken(t *testing.T) {
	setEnv(t, keyserviceTestEnv(map[string]string{
		"ARTIFACT_KEYSERVICE": "vault",
	}))
	if _, err := Load(); err == nil {
		t.Fatal("expected error for vault without token, got nil")
	}
}

// TestLoad_KeyService_GCPKMS_RequiresKEKAndProject locks the
// must-configure-resource-paths contract for the gcpkms tier.
// Authentication is via Application Default Credentials at runtime;
// validation here only catches the static configuration shape.
func TestLoad_KeyService_GCPKMS_RequiresKEKAndProject(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{
			"missing both",
			map[string]string{"ARTIFACT_KEYSERVICE": "gcpkms"},
		},
		{
			"missing KEK",
			map[string]string{
				"ARTIFACT_KEYSERVICE":               "gcpkms",
				"ARTIFACT_GCP_FIRESTORE_PROJECT_ID": "p",
			},
		},
		{
			"missing project",
			map[string]string{
				"ARTIFACT_KEYSERVICE":           "gcpkms",
				"ARTIFACT_GCP_KMS_KEK_RESOURCE": "projects/p/locations/l/keyRings/r/cryptoKeys/k",
			},
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			setEnv(t, keyserviceTestEnv(c.env))
			if _, err := Load(); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

// TestLoad_KeyService_GCPKMS_AcceptsCompleteConfig pins the happy
// path: with both required fields set, the load succeeds and the
// optional fields receive their documented defaults.
func TestLoad_KeyService_GCPKMS_AcceptsCompleteConfig(t *testing.T) {
	setEnv(t, keyserviceTestEnv(map[string]string{
		"ARTIFACT_KEYSERVICE":               "gcpkms",
		"ARTIFACT_GCP_KMS_KEK_RESOURCE":     "projects/test/locations/us/keyRings/r/cryptoKeys/k",
		"ARTIFACT_GCP_FIRESTORE_PROJECT_ID": "test",
	}))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.KeyService != "gcpkms" {
		t.Errorf("KeyService: want gcpkms, got %q", cfg.KeyService)
	}
	if cfg.GCPFirestoreDatabase != "(default)" {
		t.Errorf("GCPFirestoreDatabase default: want (default), got %q", cfg.GCPFirestoreDatabase)
	}
	if cfg.GCPFirestoreCollection != "ortholog-artifact-keys" {
		t.Errorf("GCPFirestoreCollection default: want ortholog-artifact-keys, got %q", cfg.GCPFirestoreCollection)
	}
}

// TestLoad_KeyService_PKCS11_RequiresLabelAndPin locks the
// must-configure-token-credentials contract for the pkcs11 tier.
// ModulePath has a default so it isn't checked here.
func TestLoad_KeyService_PKCS11_RequiresLabelAndPin(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{
			"missing both",
			map[string]string{"ARTIFACT_KEYSERVICE": "pkcs11"},
		},
		{
			"missing pin",
			map[string]string{
				"ARTIFACT_KEYSERVICE":         "pkcs11",
				"ARTIFACT_PKCS11_TOKEN_LABEL": "ortholog",
			},
		},
		{
			"missing label",
			map[string]string{
				"ARTIFACT_KEYSERVICE": "pkcs11",
				"ARTIFACT_PKCS11_PIN": "1234",
			},
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			setEnv(t, keyserviceTestEnv(c.env))
			if _, err := Load(); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

// TestLoad_KeyService_Memory_NoExtraConfigRequired pins the
// dev-mode escape hatch: ARTIFACT_KEYSERVICE=memory needs nothing
// else and works in any environment.
func TestLoad_KeyService_Memory_NoExtraConfigRequired(t *testing.T) {
	setEnv(t, keyserviceTestEnv(map[string]string{
		"ARTIFACT_KEYSERVICE": "memory",
	}))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.KeyService != "memory" {
		t.Errorf("KeyService: want memory, got %q", cfg.KeyService)
	}
}

// TestLoad_KeyService_RejectsUnknown locks the validation contract
// — typos and removed names must error at Load(), not at first-use.
func TestLoad_KeyService_RejectsUnknown(t *testing.T) {
	for _, name := range []string{"vaultenterprise", "kms", "hsm", "vaul", "gcp"} {
		name := name
		t.Run(name, func(t *testing.T) {
			setEnv(t, keyserviceTestEnv(map[string]string{
				"ARTIFACT_KEYSERVICE": name,
			}))
			if _, err := Load(); err == nil {
				t.Fatalf("expected error for keyservice %q, got nil", name)
			}
		})
	}
}
