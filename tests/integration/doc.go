/*
Package integration runs the backend conformance suite against real
protocol implementations running in Docker containers.

Why this layer exists:
  - Wave 1's HTTP-mocked tests catch wire-format bugs against OUR model
    of the vendor's API. If the vendor's real API diverges from our
    model (error codes, edge cases, auth-header quirks), Wave 1 misses
    it. Wave 2 runs the same conformance suite against the actual
    reference implementations.
  - These tests exercise the docker library path, content-negotiation,
    streaming, and error classification that can only be validated
    against a real server speaking the real protocol.

What this layer does NOT cover (deferred to Wave 3):
  - Vendor-specific quirks that ONLY appear in cloud production
    environments (IAM, STS, VPC endpoints, regional latency, rate
    limiting). Wave 3 runs against Filebase, real AWS, real GCS.
  - Container images lag the vendor's production API by months to years.
    A passing Wave 2 does not prove the behavior in production cloud.

Build tag:
  Every file in this package carries  //go:build integration
  so that `go test ./...` (the Wave 1 developer flow) doesn't try to
  start Docker containers. CI runs `go test -tags=integration ./tests/integration/...`
  in a separate job (see .github/workflows/integration.yml).

Prerequisites:
  - Docker daemon reachable (DOCKER_HOST env or /var/run/docker.sock)
  - Pull access to: minio/minio, fsouza/fake-gcs-server, ipfs/kubo

  If Docker is unreachable, TestMain bails out with a clear message.
  Tests do NOT silently skip — silently-skipped integration tests are
  the exact anti-pattern TESTING.md prohibits.

Runtime:
  ~90 seconds total for the full integration suite, dominated by
  container startup (each pulls, boots, waits for readiness).
  Tests reuse containers across scenarios within a single run to amortize
  startup cost.
*/
package integration
