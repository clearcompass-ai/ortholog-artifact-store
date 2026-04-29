#!/usr/bin/env bash
# scripts/run-scale-gcs.sh
#
# Standalone load test against real GCS. Pushes SCALE_N (default 10_000)
# mixed-size objects (1 KiB / 64 KiB / 1 MiB) through an in-process
# api.NewMux server wired to a real GCSBackend, samples a read-back,
# then deletes everything it pushed.
#
# Auth: ADC only. Run once on your workstation:
#
#     gcloud auth application-default login
#
# This script then mints a short-lived access token via
#     gcloud auth application-default print-access-token
# and passes it to the test as STAGING_GCS_ACCESS_TOKEN.
#
# A service-account JSON key is NOT required — the scale test only
# exercises Push / Fetch / Delete, which the GCS JSON API authorises
# from a Bearer token alone. (V4 signed-URL minting needs a private
# key, but the scale path never calls /resolve.)
#
# Required env:
#   STAGING_GCS_BUCKET     — target bucket (must have a lifecycle
#                             rule reaping staging/* after 24 h)
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

if ! command -v gcloud >/dev/null 2>&1; then
  echo "FATAL: gcloud not found in PATH. Install the Google Cloud CLI" >&2
  echo "       and run 'gcloud auth application-default login' once." >&2
  exit 2
fi

# Mint a short-lived access token from ADC. If the user hasn't run
# `gcloud auth application-default login`, this fails with a gcloud
# error message that points them at exactly that command.
echo "==> minting GCS access token from ADC"
STAGING_GCS_ACCESS_TOKEN="$(gcloud auth application-default print-access-token 2>&1)" || {
  echo "FATAL: could not mint an access token from ADC. Output above." >&2
  echo "       If this is the first run, do:" >&2
  echo "           gcloud auth application-default login" >&2
  exit 2
}
export STAGING_GCS_ACCESS_TOKEN

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
