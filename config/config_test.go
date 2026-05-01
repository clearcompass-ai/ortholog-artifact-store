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
	return osLookupEnv(k)
}
func setenv(k, v string) error { return osSetenv(k, v) }
func unsetenv(k string) error  { return osUnsetenv(k) }

// ─── Backend validation matrix ───────────────────────────────────────

func TestLoad_BackendValidation(t *testing.T) {
	cases := []struct {
		name    string
		backend string
		wantErr bool
	}{
		{"memory", "memory", false},
		{"gcs", "gcs", false},
		{"rustfs", "rustfs", false},
		{"empty_defaults_to_memory", "", false},
		{"s3_no_longer_accepted", "s3", true},
		{"ipfs_no_longer_accepted", "ipfs", true},
		{"unknown_errors", "postgres", true},
		{"typo_errors", "gcss", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			setEnv(t, map[string]string{
				"ARTIFACT_BACKEND":        tc.backend,
				"ARTIFACT_MIRROR_BACKEND": "",
				"ARTIFACT_MIRROR_MODE":    "",
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
		{"empty_disables_mirror", "", false},
		{"memory_is_invalid_as_mirror", "memory", true},
		{"s3_no_longer_accepted", "s3", true},
		{"ipfs_no_longer_accepted", "ipfs", true},
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

func TestLoad_MirrorMode_OnlySyncAccepted(t *testing.T) {
	// Sync is the only supported mode. The pre-v7.75 async_pin mode
	// targeted IPFS-IPFS replication and is gone with IPFS.
	cases := []struct {
		mode    string
		wantErr bool
	}{
		{"sync", false},
		{"", false}, // empty defaults to "sync" via envOrDefault
		{"async_pin", true},
		{"eventually-maybe", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.mode, func(t *testing.T) {
			setEnv(t, map[string]string{
				"ARTIFACT_BACKEND":     "memory",
				"ARTIFACT_MIRROR_MODE": tc.mode,
			})
			_, err := Load()
			if tc.wantErr && err == nil {
				t.Fatalf("want error for mode=%q, got nil", tc.mode)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for mode=%q: %v", tc.mode, err)
			}
		})
	}
}

// ─── Defaults ────────────────────────────────────────────────────────

func TestLoad_Defaults(t *testing.T) {
	setEnv(t, map[string]string{
		"ARTIFACT_BACKEND":              "",
		"ARTIFACT_ENDPOINT":             "",
		"ARTIFACT_BUCKET":               "",
		"ARTIFACT_REGION":               "",
		"ARTIFACT_PATH_STYLE":           "",
		"ARTIFACT_PREFIX":               "",
		"ARTIFACT_MIRROR_BACKEND":       "",
		"ARTIFACT_MIRROR_ENDPOINT":      "",
		"ARTIFACT_MIRROR_BUCKET":        "",
		"ARTIFACT_MIRROR_MODE":          "",
		"ARTIFACT_VERIFY_ON_PUSH":       "",
		"ARTIFACT_RESOLVE_EXPIRY":       "",
		"ARTIFACT_LISTEN_ADDR":          "",
		"ARTIFACT_MAX_BODY_SIZE":        "",
		"ORTHOLOG_ENV":                  "",
		"ARTIFACT_REQUIRE_UPLOAD_TOKEN": "",
		"ARTIFACT_OPERATOR_PUBKEYS":     "",
		"ARTIFACT_OPERATOR_PUBKEYS_DIR": "",
		// This test exercises storage-backend defaults; opt out of
		// the keyservice "vault" default (which requires a token)
		// so we don't need to fabricate one here. Keyservice defaults
		// have their own coverage in TestLoad_KeyService_*.
		"ARTIFACT_KEYSERVICE": "memory",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load defaults: %v", err)
	}
	if cfg.Backend != "memory" {
		t.Errorf("Backend: want memory, got %q", cfg.Backend)
	}
	if cfg.Endpoint != "" {
		t.Errorf("Endpoint: want empty, got %q", cfg.Endpoint)
	}
	if cfg.Bucket != "ortholog-artifacts" {
		t.Errorf("Bucket: want ortholog-artifacts, got %q", cfg.Bucket)
	}
	if cfg.Region != "us-east-1" {
		t.Errorf("Region: want us-east-1, got %q", cfg.Region)
	}
	if cfg.PathStyle {
		t.Errorf("PathStyle: want false, got true")
	}
	if cfg.Prefix != "" {
		t.Errorf("Prefix: want empty, got %q", cfg.Prefix)
	}
	if cfg.MirrorBackend != "" {
		t.Errorf("MirrorBackend: want empty, got %q", cfg.MirrorBackend)
	}
	if cfg.MirrorEndpoint != "" {
		t.Errorf("MirrorEndpoint: want empty, got %q", cfg.MirrorEndpoint)
	}
	if cfg.MirrorBucket != "" {
		t.Errorf("MirrorBucket: want empty, got %q", cfg.MirrorBucket)
	}
	if cfg.MirrorMode != "sync" {
		t.Errorf("MirrorMode: want sync, got %q", cfg.MirrorMode)
	}
	if !cfg.VerifyOnPush {
		t.Errorf("VerifyOnPush: want true, got false")
	}
	if cfg.Env != "dev" {
		t.Errorf("Env: want dev, got %q", cfg.Env)
	}
	if cfg.RequireUploadToken != "off" {
		t.Errorf("RequireUploadToken: want off, got %q", cfg.RequireUploadToken)
	}
	if cfg.OperatorPubKeys != "" {
		t.Errorf("OperatorPubKeys: want empty, got %q", cfg.OperatorPubKeys)
	}
	if cfg.OperatorPubKeysDir != "" {
		t.Errorf("OperatorPubKeysDir: want empty, got %q", cfg.OperatorPubKeysDir)
	}
	if cfg.DefaultResolveExpiry != 3600*time.Second {
		t.Errorf("DefaultResolveExpiry: want 1h, got %v", cfg.DefaultResolveExpiry)
	}
	if cfg.ListenAddr != ":8082" {
		t.Errorf("ListenAddr: want :8082, got %q", cfg.ListenAddr)
	}
	if cfg.MaxBodySize != 64<<20 {
		t.Errorf("MaxBodySize: want 64 MiB, got %d", cfg.MaxBodySize)
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
		"ARTIFACT_MAX_BODY_SIZE": "1048576", // 1 MiB
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxBodySize != 1<<20 {
		t.Fatalf("MaxBodySize: want %d, got %d", 1<<20, cfg.MaxBodySize)
	}
}

func TestLoad_MaxBodySize_MalformedFallsBackToDefault(t *testing.T) {
	// envInt64 returns the fallback on parse failure; the validator
	// then never gets a chance to clamp because the raw value never
	// enters the Config in the first place.
	setEnv(t, map[string]string{
		"ARTIFACT_BACKEND":       "memory",
		"ARTIFACT_MAX_BODY_SIZE": "not-a-number",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxBodySize != 64<<20 {
		t.Fatalf("malformed MaxBodySize should fall back to 64 MiB, got %d", cfg.MaxBodySize)
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
		{"", true},       // default
		{"banana", true}, // malformed falls back to default (true)
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

func TestLoad_PathStyle_Parsing(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"true", true},
		{"false", false},
		{"1", true},
		{"0", false},
		{"", false},        // default for PathStyle is false
		{"not-bool", false}, // malformed falls back
	}
	for _, tc := range cases {
		tc := tc
		t.Run("raw_"+tc.raw, func(t *testing.T) {
			setEnv(t, map[string]string{
				"ARTIFACT_BACKEND":    "memory",
				"ARTIFACT_PATH_STYLE": tc.raw,
			})
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.PathStyle != tc.want {
				t.Fatalf("raw=%q: PathStyle want %v, got %v", tc.raw, tc.want, cfg.PathStyle)
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

// ─── Custom values pass through where defaults would normally apply ──

func TestLoad_StringEnvPassThrough(t *testing.T) {
	// envOrDefault: a non-empty string env wins over the fallback.
	setEnv(t, map[string]string{
		"ARTIFACT_BACKEND":     "rustfs",
		"ARTIFACT_BUCKET":      "my-bucket",
		"ARTIFACT_REGION":      "eu-west-2",
		"ARTIFACT_PREFIX":      "tenant-A/",
		"ARTIFACT_LISTEN_ADDR": ":9000",
		"ARTIFACT_ENDPOINT":    "http://rustfs.internal:9000",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Backend != "rustfs" {
		t.Errorf("Backend: want rustfs, got %q", cfg.Backend)
	}
	if cfg.Bucket != "my-bucket" {
		t.Errorf("Bucket: want my-bucket, got %q", cfg.Bucket)
	}
	if cfg.Region != "eu-west-2" {
		t.Errorf("Region: want eu-west-2, got %q", cfg.Region)
	}
	if cfg.Prefix != "tenant-A/" {
		t.Errorf("Prefix: want tenant-A/, got %q", cfg.Prefix)
	}
	if cfg.ListenAddr != ":9000" {
		t.Errorf("ListenAddr: want :9000, got %q", cfg.ListenAddr)
	}
	if cfg.Endpoint != "http://rustfs.internal:9000" {
		t.Errorf("Endpoint: want http://rustfs.internal:9000, got %q", cfg.Endpoint)
	}
}

// ─── AS-2: upload-token policy validation ────────────────────────────

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

func TestLoad_OperatorPubKeysPassthrough(t *testing.T) {
	// Config doesn't validate the pubkey strings — the cmd-side loader
	// does. Config just passes them through. Verify both env vars land
	// on the right struct fields.
	setEnv(t, map[string]string{
		"ARTIFACT_BACKEND":              "memory",
		"ARTIFACT_OPERATOR_PUBKEYS":     "kid1:abc,kid2:def",
		"ARTIFACT_OPERATOR_PUBKEYS_DIR": "/etc/artifact/keys",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OperatorPubKeys != "kid1:abc,kid2:def" {
		t.Errorf("OperatorPubKeys: want kid1:abc,kid2:def, got %q", cfg.OperatorPubKeys)
	}
	if cfg.OperatorPubKeysDir != "/etc/artifact/keys" {
		t.Errorf("OperatorPubKeysDir: want /etc/artifact/keys, got %q", cfg.OperatorPubKeysDir)
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
