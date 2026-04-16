#!/usr/bin/env bash
# scripts/test-all.sh
#
# Run every Wave 1 layer in order. CI calls this on every PR.
# Fast fail — if any layer fails, stop immediately.
#
# Wave 2 will add: test-integration (testcontainers)
# Wave 3 will add: test-staging (real cloud, only runs in nightly.yml)

set -euo pipefail

cd "$(dirname "$0")/.."

echo "━━━ 1/4 lint ━━━"
make lint

echo
echo "━━━ 2/4 test (unit + HTTP-mocked, -race) ━━━"
make test

echo
echo "━━━ 3/4 coverage gate (≥80% per package) ━━━"
make coverage-gate

echo
echo "━━━ 4/4 fuzz (30s per target) ━━━"
make fuzz

echo
echo "✓ all Wave 1 layers passed"
