//go:build staging

package staging

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestMain is the Wave 3 entry point. Its sole job is to verify that
// the credentials required for EACH vendor suite are available. When
// missing, it fails loudly — the caller may have chosen to run only
// some vendor suites, which is fine, but any suite that's enabled by
// its build tag must have credentials or we refuse to run.
//
// The policy is: we DO NOT skip tests for missing credentials. If the
// staging tag was passed, the operator asked for real-cloud validation.
// Silently skipping a vendor because its credentials are absent would
// mask the exact signal Wave 3 exists to provide.
//
// Specific vendor suites each read their own env vars in their setup.
// TestMain just validates the shape: if the `STAGING_` prefix exists
// at all, refuse to start with a partial credential set.
func TestMain(m *testing.M) {
	// Check the universal var first: STAGING_ENABLED must be set to "1".
	// This is belt-and-suspenders on top of the build tag — it prevents
	// an accidental  go test -tags=staging  in the wrong shell from
	// lighting up real cloud bills.
	if os.Getenv("STAGING_ENABLED") != "1" {
		fmt.Fprintln(os.Stderr,
			"FATAL: Wave 3 staging tests require STAGING_ENABLED=1.\n"+
				"       This is an intentional second gate on top of -tags=staging\n"+
				"       so  go test -tags=staging  on a dev laptop does not hit real\n"+
				"       cloud APIs by accident. Set STAGING_ENABLED=1 in CI only.")
		os.Exit(2)
	}

	// Validate per-vendor credential sets. A vendor is considered "in
	// scope" if ANY of its env vars is set; all others must then also be
	// set. Partial credentials are a configuration bug.
	var problems []string
	for _, vendor := range []credentialGroup{
		{
			name: "GCS",
			keys: []string{
				"STAGING_GCS_BUCKET",
				"STAGING_GCS_SERVICE_ACCOUNT_JSON",
			},
		},
		{
			// Filebase is exercised through its IPFS pinning gateway only.
			// (S3-protocol vendor sprawl is out of scope for this repo —
			// the supported S3-protocol implementation is RustFS, exercised
			// in Wave 2 against a containerized RustFS, not in Wave 3
			// against any real-cloud S3-compatible vendor.)
			name: "Filebase IPFS",
			keys: []string{
				"STAGING_FILEBASE_IPFS_TOKEN",
			},
		},
	} {
		if err := vendor.validate(); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) > 0 {
		fmt.Fprintln(os.Stderr, "FATAL: Wave 3 credential validation failed:")
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "  - "+p)
		}
		os.Exit(2)
	}

	os.Exit(m.Run())
}

type credentialGroup struct {
	name string
	keys []string
}

// validate checks that either ALL env vars are set or NONE are. A
// partial set is always a misconfiguration.
func (g credentialGroup) validate() error {
	var present, absent []string
	for _, k := range g.keys {
		if os.Getenv(k) != "" {
			present = append(present, k)
		} else {
			absent = append(absent, k)
		}
	}
	if len(present) == 0 {
		return nil // vendor not enabled, fine
	}
	if len(absent) > 0 {
		return fmt.Errorf("%s partially configured; missing: %s",
			g.name, strings.Join(absent, ", "))
	}
	return nil
}

// Vendor-gating helpers used by each suite file to skip cleanly if a
// vendor was not configured in this run. These are the ONLY places a
// test may "skip" in Wave 3 — and the skip reason is always logged at
// t.Logf so the reason appears in the CI summary.

func gcsConfigured() bool          { return os.Getenv("STAGING_GCS_BUCKET") != "" }
func filebaseIPFSConfigured() bool { return os.Getenv("STAGING_FILEBASE_IPFS_TOKEN") != "" }
