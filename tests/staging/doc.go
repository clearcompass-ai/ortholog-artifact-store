/*
Package staging runs the backend conformance suite against real cloud
production APIs.

Why this layer exists:
  - Wave 1 (HTTP-mocked) validates our model of the vendor's API
  - Wave 2 (containers) validates our model against the vendor's reference
    implementation running locally
  - Wave 3 (this layer) validates against actual cloud production APIs,
    where IAM, regional endpoints, eventual consistency, and vendor-
    specific rate limiting can break things that both prior layers missed

Container images lag production APIs by months. fake-gcs-server does
not model the GCS signed-URL format accurately. Wave 3 is the layer
that actually catches production regressions for the one cloud-coupled
backend in scope (real GCS). The S3-protocol path is exercised in
Wave 2 against a containerized RustFS — real-cloud S3 vendors (AWS,
Wasabi, R2, Filebase-S3) are not in the supported set and intentionally
not tested here. IPFS is no longer a supported backend kind; the prior
Filebase-IPFS staging suite is gone.

What this layer IS NOT:
  - A load test (see load-generator repo, not in this codebase)
  - A deployment smoke test (that's a post-deploy canary, not Wave 3)
  - A continuous production monitor (that's observability, not Wave 3)

Cost and scheduling:
  - Runs nightly, never per-PR
  - Dedicated credentials scoped to a single test bucket per vendor
  - Every test run uses a unique key prefix so parallel runs don't collide
  - Lifecycle rules delete test objects after 24 hours (set at bucket level,
    configured outside this test code)
  - Hard budget cap: $20/month across all Wave 3 accounts (enforced via
    billing alerts on the vendor side, not the test code)

Credentials (loaded from env, fail loudly if absent when Wave 3 is
invoked):
  GCS: STAGING_GCS_BUCKET, STAGING_GCS_SERVICE_ACCOUNT_JSON
       (path to service-account JSON file)

Build tag:
  Every file carries  //go:build staging
  so that neither  `go test ./...`  (Wave 1)  nor  `go test -tags=integration ./...`
  (Wave 2) pulls in this package. Wave 3 is explicitly opted into with
  `go test -tags=staging ./tests/staging/...`.

Failure mode policy:
  If credentials are missing, TestMain calls  os.Exit(2)  with a clear
  message naming which env vars are missing. No silent skipping — a
  silently-skipped Wave 3 run is worse than none at all, because it
  trains operators to stop watching the nightly signal.
*/
package staging
