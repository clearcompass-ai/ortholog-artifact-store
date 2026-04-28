package staging

import (
	"strings"
	"testing"
)

// credValidate_ScopeNotEnabled — when none of the keys are set, the
// vendor is "not enabled in this run" and validation passes silently.
func TestCredentialGroup_Validate_NoneSet_ReturnsNil(t *testing.T) {
	clearEnv(t, "TEST_CRED_A", "TEST_CRED_B")

	g := credentialGroup{name: "X", keys: []string{"TEST_CRED_A", "TEST_CRED_B"}}
	if err := g.validate(); err != nil {
		t.Fatalf("none set: want nil, got %v", err)
	}
}

// All keys set → vendor enabled and configured. validate() returns nil.
func TestCredentialGroup_Validate_AllSet_ReturnsNil(t *testing.T) {
	clearEnv(t, "TEST_CRED_A", "TEST_CRED_B")
	t.Setenv("TEST_CRED_A", "value-a")
	t.Setenv("TEST_CRED_B", "value-b")

	g := credentialGroup{name: "X", keys: []string{"TEST_CRED_A", "TEST_CRED_B"}}
	if err := g.validate(); err != nil {
		t.Fatalf("all set: want nil, got %v", err)
	}
}

// Partial set → error naming the missing keys.
func TestCredentialGroup_Validate_PartialSet_ReturnsError(t *testing.T) {
	clearEnv(t, "TEST_CRED_A", "TEST_CRED_B", "TEST_CRED_C")
	t.Setenv("TEST_CRED_A", "value-a")
	// TEST_CRED_B and TEST_CRED_C deliberately unset

	g := credentialGroup{name: "PartialVendor", keys: []string{"TEST_CRED_A", "TEST_CRED_B", "TEST_CRED_C"}}
	err := g.validate()
	if err == nil {
		t.Fatal("partial set: want error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "PartialVendor") {
		t.Errorf("error should name the vendor: %q", msg)
	}
	if !strings.Contains(msg, "TEST_CRED_B") {
		t.Errorf("error should name missing TEST_CRED_B: %q", msg)
	}
	if !strings.Contains(msg, "TEST_CRED_C") {
		t.Errorf("error should name missing TEST_CRED_C: %q", msg)
	}
	if strings.Contains(msg, "TEST_CRED_A") {
		t.Errorf("error should NOT name the present key TEST_CRED_A: %q", msg)
	}
}

// Empty keys list (no required env vars) — degenerate but well-defined.
// A group with zero keys is always neither "configured" nor "missing
// anything" — validate must return nil.
func TestCredentialGroup_Validate_EmptyKeys_ReturnsNil(t *testing.T) {
	g := credentialGroup{name: "Empty", keys: nil}
	if err := g.validate(); err != nil {
		t.Fatalf("empty keys: want nil, got %v", err)
	}
}

// Whitespace-only env value still counts as "set" (os.Getenv returns
// the literal value). The validator's "absent" branch only fires on
// the empty string. This pins the contract: callers must not depend
// on whitespace being treated as missing.
func TestCredentialGroup_Validate_WhitespaceCountsAsSet(t *testing.T) {
	clearEnv(t, "TEST_CRED_A", "TEST_CRED_B")
	t.Setenv("TEST_CRED_A", "value")
	t.Setenv("TEST_CRED_B", "   ") // whitespace, but not empty

	g := credentialGroup{name: "WS", keys: []string{"TEST_CRED_A", "TEST_CRED_B"}}
	if err := g.validate(); err != nil {
		t.Fatalf("whitespace value: want nil, got %v", err)
	}
}

// clearEnv unsets every named variable for the duration of the test,
// restoring the prior values via t.Cleanup. Necessary because t.Setenv
// only sets — to ensure a "not present" baseline we have to remember
// and restore.
func clearEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		t.Setenv(k, "") // ensure cleanup is registered
		// Setenv("") leaves the var as "" (not unset). Use os.Unsetenv
		// directly via the staging-package alias so the value is truly
		// absent for os.Getenv during the test body.
		_ = unsetEnvDirect(k)
	}
}
