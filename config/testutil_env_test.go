package config

import (
	"os"
	"testing"
)

// Thin wrappers so config_test.go can reference env operations without
// `os` imports cluttering up every case. Kept in a separate file so
// production code isn't polluted with test-only indirection.

func osLookupEnv(k string) (string, bool) { return os.LookupEnv(k) }
func osSetenv(k, v string) error          { return os.Setenv(k, v) }
func osUnsetenv(k string) error           { return os.Unsetenv(k) }

// TestMain pins the keyservice selection to "memory" for the entire
// config-package test run. The package's pre-existing tests focus
// on the storage-backend matrix; they don't (and shouldn't) have
// to plumb a Vault token through every t.Run subcase just because
// the keyservice default is "vault" in production. Tests that
// specifically exercise keyservice validation override the env
// var inside their own setEnv map (see keyservice_config_test.go).
func TestMain(m *testing.M) {
	_ = os.Setenv("ARTIFACT_KEYSERVICE", "memory")
	os.Exit(m.Run())
}
