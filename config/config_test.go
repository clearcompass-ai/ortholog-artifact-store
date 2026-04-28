package config

import (
	"testing"
	"time"
)

// setEnv sets env vars for the duration of the test, restoring originals
// (or unsetting them if absent) via t.Cleanup. Safer than manual defer
// because it survives panics and doesn't require bookkeeping in tests.
func setEnv(t *testing.T, vars map[string]string) {
	t.Helper()
	for k, v := range vars {
		originalVal, existed := lookupEnv(k)
		if err := setenv(k, v); err != nil {
			t.Fatalf("setenv %s: %v", k, err)
		}
		k := k
		t.Cleanup(func() {
			if existed {
				_ = setenv(k, originalVal)
			} else {
				_ = unsetenv(k)
			}
		})
	}
}

// Indirection so the test file doesn't need os.* imports strewn around.
func lookupEnv(k string) (string, bool) {
	// Deliberately uses os.LookupEnv via a small helper so signature is clean.
	return osLookupEnv(k)
}
func setenv(k, v string) error { return osSetenv(k, v) }
func unsetenv(k string) error  { return osUnsetenv(k) }

// ─── Backend validation matrix ───────────────────────────────────────

func TestLoad_BackendValidation(t *testing.T) {
	cases := []struct {
		name     string
		backend  string
		wantErr  bool
	}{
		{"memory", "memory", false},
		{"gcs", "gcs", false},
		{"rustfs", "rustfs", false},
		{"ipfs", "ipfs", false},
		{"empty_defaults_to_memory", "", false},
		{"s3_no_longer_accepted", "s3", true},
		{"unknown_errors", "postgres", true},
		{"typo_errors", "gcss", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Clear ALL mirror-related envs to avoid leaks between tests.
			setEnv(t, map[string]string{
				"ARTIFACT_BACKEND":          tc.backend,
				"ARTIFACT_MIRROR_BACKEND":   "",
				"ARTIFACT_MIRROR_MODE":      "",
			})
			cfg, err := Load()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error for backend=%q, got nil; cfg=%+v", tc.backend, cfg)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for backend=%q: %v", tc.backend, err)
			}
			if tc.backend != "" && cfg.Backend != tc.backend {
				t.Fatalf("Backend: want %q, got %q", tc.backend, cfg.Backend)
			}
			if tc.backend == "" && cfg.Backend != "memory" {
				t.Fatalf("empty backend should default to memory, got %q", cfg.Backend)
			}
		})
	}
}

// ─── Mirror validation matrix ────────────────────────────────────────

func TestLoad_MirrorBackendValidation(t *testing.T) {
	cases := []struct {
		name    string
		mirror  string
		wantErr bool
	}{
		{"gcs", "gcs", false},
		{"rustfs", "rustfs", false},
		{"ipfs", "ipfs", false},
		{"empty_disables_mirror", "", false},
		{"memory_is_invalid_as_mirror", "memory", true},
		{"s3_no_longer_accepted", "s3", true},
		{"unknown_errors", "ftp", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			setEnv(t, map[string]string{
				"ARTIFACT_BACKEND":        "gcs",
				"ARTIFACT_MIRROR_BACKEND": tc.mirror,
				"ARTIFACT_MIRROR_MODE":    "sync",
			})
			_, err := Load()
			if tc.wantErr && err == nil {
				t.Fatalf("want error for mirror=%q, got nil", tc.mirror)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for mirror=%q: %v", tc.mirror, err)
			}
		})
	}
}

// ─── Async-pin requires both IPFS ────────────────────────────────────

func TestLoad_AsyncPinRequiresIPFSBoth(t *testing.T) {
	cases := []struct {
		name    string
		primary string
		mirror  string
		wantErr bool
	}{
		{"both_ipfs_ok", "ipfs", "ipfs", false},
		{"primary_gcs_errors", "gcs", "ipfs", true},
		{"mirror_gcs_errors", "ipfs", "gcs", true},
		{"both_gcs_errors", "gcs", "gcs", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			setEnv(t, map[string]string{
				"ARTIFACT_BACKEND":        tc.primary,
				"ARTIFACT_MIRROR_BACKEND": tc.mirror,
				"ARTIFACT_MIRROR_MODE":    "async_pin",
			})
			_, err := Load()
			if tc.wantErr && err == nil {
				t.Fatalf("want error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoad_InvalidMirrorMode(t *testing.T) {
	setEnv(t, map[string]string{
		"ARTIFACT_BACKEND":     "memory",
		"ARTIFACT_MIRROR_MODE": "eventually-maybe",
	})
	_, err := Load()
	if err == nil {
		t.Fatal("invalid mirror mode should error")
	}
}

// ─── Defaults ────────────────────────────────────────────────────────

func TestLoad_Defaults(t *testing.T) {
	// Clear every env var we care about to prove defaults apply.
	setEnv(t, map[string]string{
		"ARTIFACT_BACKEND":                 "",
		"ARTIFACT_ENDPOINT":                "",
		"ARTIFACT_BUCKET":                  "",
		"ARTIFACT_REGION":                  "",
		"ARTIFACT_PATH_STYLE":              "",
		"ARTIFACT_PREFIX":                  "",
		"ARTIFACT_IPFS_GATEWAY":            "",
		"ARTIFACT_MIRROR_BACKEND":          "",
		"ARTIFACT_MIRROR_MODE":             "",
		"ARTIFACT_VERIFY_ON_PUSH":          "",
		"ARTIFACT_RESOLVE_EXPIRY":          "",
		"ARTIFACT_LISTEN_ADDR":             "",
		"ARTIFACT_MAX_BODY_SIZE":           "",
		"ORTHOLOG_ENV":                     "",
		"ARTIFACT_REQUIRE_UPLOAD_TOKEN":    "",
		"ARTIFACT_OPERATOR_PUBKEY":         "",
		"ARTIFACT_OPERATOR_PUBKEY_FILE":    "",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load defaults: %v", err)
	}
	wants := map[string]any{
		"Backend":              "memory",
		"Bucket":               "ortholog-artifacts",
		"Region":               "us-east-1",
		"PathStyle":            false,
		"IPFSGateway":          "https://ipfs.io",
		"MirrorMode":           "sync",
		"VerifyOnPush":         true,
		"Env":                  "dev",
		"RequireUploadToken":   "off",
		"DefaultResolveExpiry": 3600 * time.Second,
		"ListenAddr":           ":8082",
		"MaxBodySize":          int64(64 << 20),
	}
	if cfg.Backend != wants["Backend"].(string) {
		t.Errorf("Backend: want %q, got %q", wants["Backend"], cfg.Backend)
	}
	if cfg.Bucket != wants["Bucket"].(string) {
		t.Errorf("Bucket: want %q, got %q", wants["Bucket"], cfg.Bucket)
	}
	if cfg.Region != wants["Region"].(string) {
		t.Errorf("Region: want %q, got %q", wants["Region"], cfg.Region)
	}
	if cfg.PathStyle != wants["PathStyle"].(bool) {
		t.Errorf("PathStyle: want %v, got %v", wants["PathStyle"], cfg.PathStyle)
	}
	if cfg.IPFSGateway != wants["IPFSGateway"].(string) {
		t.Errorf("IPFSGateway: want %q, got %q", wants["IPFSGateway"], cfg.IPFSGateway)
	}
	if cfg.MirrorMode != wants["MirrorMode"].(string) {
		t.Errorf("MirrorMode: want %q, got %q", wants["MirrorMode"], cfg.MirrorMode)
	}
	if cfg.VerifyOnPush != wants["VerifyOnPush"].(bool) {
		t.Errorf("VerifyOnPush: want %v, got %v", wants["VerifyOnPush"], cfg.VerifyOnPush)
	}
	if cfg.Env != wants["Env"].(string) {
		t.Errorf("Env: want %q, got %q", wants["Env"], cfg.Env)
	}
	if cfg.RequireUploadToken != wants["RequireUploadToken"].(string) {
		t.Errorf("RequireUploadToken: want %q, got %q", wants["RequireUploadToken"], cfg.RequireUploadToken)
	}
	if cfg.DefaultResolveExpiry != wants["DefaultResolveExpiry"].(time.Duration) {
		t.Errorf("DefaultResolveExpiry: want %v, got %v",
			wants["DefaultResolveExpiry"], cfg.DefaultResolveExpiry)
	}
	if cfg.ListenAddr != wants["ListenAddr"].(string) {
		t.Errorf("ListenAddr: want %q, got %q", wants["ListenAddr"], cfg.ListenAddr)
	}
	if cfg.MaxBodySize != wants["MaxBodySize"].(int64) {
		t.Errorf("MaxBodySize: want %d, got %d", wants["MaxBodySize"], cfg.MaxBodySize)
	}
}

// ─── Env var parsing edge cases ──────────────────────────────────────

func TestLoad_MaxBodySize_ZeroFallsBackToDefault(t *testing.T) {
	setEnv(t, map[string]string{
		"ARTIFACT_BACKEND":       "memory",
		"ARTIFACT_MAX_BODY_SIZE": "0",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxBodySize != 64<<20 {
		t.Fatalf("MaxBodySize=0 should fall back to 64MB, got %d", cfg.MaxBodySize)
	}
}

func TestLoad_MaxBodySize_NegativeFallsBackToDefault(t *testing.T) {
	setEnv(t, map[string]string{
		"ARTIFACT_BACKEND":       "memory",
		"ARTIFACT_MAX_BODY_SIZE": "-1",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxBodySize != 64<<20 {
		t.Fatalf("MaxBodySize=-1 should fall back to 64MB, got %d", cfg.MaxBodySize)
	}
}

func TestLoad_MaxBodySize_Custom(t *testing.T) {
	setEnv(t, map[string]string{
		"ARTIFACT_BACKEND":       "memory",
		"ARTIFACT_MAX_BODY_SIZE": "1048576", // 1 MB
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxBodySize != 1<<20 {
		t.Fatalf("MaxBodySize: want %d, got %d", 1<<20, cfg.MaxBodySize)
	}
}

func TestLoad_VerifyOnPush_Parsing(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"true", true},
		{"false", false},
		{"1", true},
		{"0", false},
		{"TRUE", true},
		{"", true},         // default
		{"banana", true},   // malformed falls back to default (true)
	}
	for _, tc := range cases {
		tc := tc
		t.Run("raw_"+tc.raw, func(t *testing.T) {
			setEnv(t, map[string]string{
				"ARTIFACT_BACKEND":        "memory",
				"ARTIFACT_VERIFY_ON_PUSH": tc.raw,
			})
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.VerifyOnPush != tc.want {
				t.Fatalf("raw=%q: VerifyOnPush want %v, got %v", tc.raw, tc.want, cfg.VerifyOnPush)
			}
		})
	}
}

func TestLoad_ResolveExpiry_Custom(t *testing.T) {
	setEnv(t, map[string]string{
		"ARTIFACT_BACKEND":        "memory",
		"ARTIFACT_RESOLVE_EXPIRY": "7200", // 2 hours
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultResolveExpiry != 2*time.Hour {
		t.Fatalf("ResolveExpiry: want 2h, got %v", cfg.DefaultResolveExpiry)
	}
}

func TestLoad_ResolveExpiry_MalformedFallsBack(t *testing.T) {
	setEnv(t, map[string]string{
		"ARTIFACT_BACKEND":        "memory",
		"ARTIFACT_RESOLVE_EXPIRY": "not-a-number",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultResolveExpiry != 3600*time.Second {
		t.Fatalf("malformed ResolveExpiry should fall back to 1h, got %v", cfg.DefaultResolveExpiry)
	}
}

// ─── AS-2: upload-token policy validation ─────────────────────────────

func TestLoad_RequireUploadToken_Validation(t *testing.T) {
	cases := []struct {
		name    string
		policy  string
		wantErr bool
	}{
		{"off", "off", false},
		{"optional", "optional", false},
		{"required", "required", false},
		{"empty_defaults_to_off", "", false},
		{"unknown_errors", "sometimes", true},
		{"typo_errors", "REQUIRED", true}, // case-sensitive by design
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			setEnv(t, map[string]string{
				"ARTIFACT_BACKEND":              "memory",
				"ARTIFACT_REQUIRE_UPLOAD_TOKEN": tc.policy,
			})
			cfg, err := Load()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error for policy=%q, got nil; cfg=%+v", tc.policy, cfg)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for policy=%q: %v", tc.policy, err)
			}
			want := tc.policy
			if want == "" {
				want = "off"
			}
			if cfg.RequireUploadToken != want {
				t.Fatalf("RequireUploadToken: want %q, got %q", want, cfg.RequireUploadToken)
			}
		})
	}
}

func TestLoad_EnvPassthrough(t *testing.T) {
	// ORTHOLOG_ENV is informational only, not validated by config —
	// the watchdog in cmd/artifact-store decides what to do with it.
	for _, v := range []string{"dev", "staging", "production", "banana"} {
		v := v
		t.Run(v, func(t *testing.T) {
			setEnv(t, map[string]string{
				"ARTIFACT_BACKEND": "memory",
				"ORTHOLOG_ENV":     v,
			})
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Env != v {
				t.Fatalf("Env: want %q, got %q", v, cfg.Env)
			}
		})
	}
}
