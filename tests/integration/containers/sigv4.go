//go:build integration

/*
containers/sigv4.go — Wave 2 type alias for the shared SigV4 signer.

The real implementation lives in internal/signers/sigv4.go, shared
between Wave 2 (this package) and Wave 3 (tests/staging/). This file
exists only to keep the historic containers.SigV4Signer type name
stable for Wave 2's callers so they didn't need churn when Wave 3
promoted the signer to internal/signers.
*/
package containers

import "github.com/clearcompass-ai/ortholog-artifact-store/internal/signers"

// SigV4Signer is an alias for the shared signers.SigV4. Wave 2 test
// code imports it as containers.SigV4Signer; the implementation lives
// under internal/signers.
type SigV4Signer = signers.SigV4
