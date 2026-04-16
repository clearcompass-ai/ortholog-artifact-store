#!/usr/bin/env bash
# scripts/flake-detector.sh
#
# Run the full test suite 50 times in a row. Any failure (even one) is
# a flake. Flakes are worse than missing tests — they train people to
# ignore failures. Fix or quarantine flakes the day they appear.
#
# Schedule: weekly in flake-detect.yml CI workflow.

set -euo pipefail

cd "$(dirname "$0")/.."

RUNS=${RUNS:-50}
LOGDIR=$(mktemp -d -t flake-XXXXXXXX)
FAILURES=0

echo "Running $RUNS iterations. Logs in $LOGDIR"

for i in $(seq 1 "$RUNS"); do
    LOG="$LOGDIR/run-$i.log"
    printf "  [%2d/%d] " "$i" "$RUNS"
    if go test -race -count=1 -timeout=60s ./... >"$LOG" 2>&1; then
        echo "PASS"
    else
        echo "FAIL  (see $LOG)"
        FAILURES=$((FAILURES + 1))
    fi
done

echo
echo "Summary: $FAILURES/$RUNS failed."
if [ "$FAILURES" -gt 0 ]; then
    echo "Flaky tests detected. Review logs in $LOGDIR"
    echo "First failing log:"
    first_fail=$(ls "$LOGDIR" | head -1)
    cat "$LOGDIR/$first_fail"
    exit 1
fi
echo "No flakes detected."
rm -rf "$LOGDIR"
