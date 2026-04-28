#!/usr/bin/env bash
# scripts/run-scale-gcs.sh
#
# Standalone load test against real GCS. Pushes SCALE_N (default 10_000)
# mixed-size objects (1 KiB / 64 KiB / 1 MiB) through an in-process
# api.NewMux server wired to a real GCSBackend, samples a read-back,
# then deletes everything it pushed.
#
# Required env:
#   STAGING_GCS_BUCKET                 — target bucket (must have a
#                                         lifecycle rule reaping
#                                         staging/* after 24 h)
#   STAGING_GCS_SERVICE_ACCOUNT_JSON   — path to service-account JSON
#   STAGING_GCS_ACCESS_TOKEN           — short-lived OAuth2 access
#                                         token (e.g. `gcloud auth
#                                         application-default
#                                         print-access-token`)
#
# Optional env (forwarded to the test):
#   SCALE_N              — object count           (default 10000)
#   SCALE_CONCURRENCY    — worker goroutines      (default 64)
#   SCALE_READBACK_PCT   — % of objects fetched   (default 1)
#
# This script sets STAGING_ENABLED=1 itself — that's the second gate
# the test binary checks on top of -tags=staging. We do it here so the
# operator can't accidentally invoke the test without realising it
# hits real cloud APIs; running this script IS the consent.

set -euo pipefail

require() {
  if [[ -z "${!1:-}" ]]; then
    echo "FATAL: $1 must be set" >&2
    exit 2
  fi
}

require STAGING_GCS_BUCKET
require STAGING_GCS_SERVICE_ACCOUNT_JSON
require STAGING_GCS_ACCESS_TOKEN

if [[ ! -f "${STAGING_GCS_SERVICE_ACCOUNT_JSON}" ]]; then
  echo "FATAL: STAGING_GCS_SERVICE_ACCOUNT_JSON points at a non-existent file: ${STAGING_GCS_SERVICE_ACCOUNT_JSON}" >&2
  exit 2
fi

export STAGING_ENABLED=1
export SCALE_N="${SCALE_N:-10000}"
export SCALE_CONCURRENCY="${SCALE_CONCURRENCY:-64}"
export SCALE_READBACK_PCT="${SCALE_READBACK_PCT:-1}"

echo "==> scale test: GCS bucket=${STAGING_GCS_BUCKET}"
echo "    SCALE_N=${SCALE_N}  SCALE_CONCURRENCY=${SCALE_CONCURRENCY}  SCALE_READBACK_PCT=${SCALE_READBACK_PCT}%"

# -timeout=60m gives a 10K-object run against real GCS plenty of
# headroom; default 10m would fight worst-case tail latency on the
# 1 MiB objects.
exec go test \
  -tags=staging \
  -count=1 \
  -timeout=60m \
  -v \
  -run='^TestScale_GCS_PushFetch$' \
  ./tests/staging/...
