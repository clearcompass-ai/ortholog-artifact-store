//go:build staging

package staging

import (
	"fmt"
	"os"
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
// Wave 3 today validates exactly one vendor: real GCS. The S3-protocol
// path is exercised in Wave 2 against a containerized RustFS, not in
// Wave 3 against any real-cloud S3-compatible vendor. IPFS is no
// longer a supported backend kind, so the prior Filebase-IPFS suite is
// gone.
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
	// scope" if ANY of its env vars is set; all others must then also
	// be set. Partial credentials are a configuration bug.
	var problems []string
	for _, vendor := range stagingVendors() {
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

// stagingVendors lists every vendor whose credential set is checked at
// Wave 3 startup. Today: GCS only. Adding a new cloud-coupled backend
// adds an entry here; the rest of the file is shape-stable.
func stagingVendors() []credentialGroup {
	return []credentialGroup{
		{
			name: "GCS",
			keys: []string{
				"STAGING_GCS_BUCKET",
				"STAGING_GCS_SERVICE_ACCOUNT_JSON",
			},
		},
	}
}

// credentialGroup and its validate() method live in credentials.go
// (non-build-tagged) so they can be unit-tested under
// `go test ./tests/staging/` without -tags=staging.

// Vendor-gating helpers used by each suite file to skip cleanly if a
// vendor was not configured in this run. These are the ONLY places a
// test may "skip" in Wave 3 — and the skip reason is always logged at
// t.Logf so the reason appears in the CI summary.

func gcsConfigured() bool { return os.Getenv("STAGING_GCS_BUCKET") != "" }
