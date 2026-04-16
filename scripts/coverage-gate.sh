#!/usr/bin/env bash
# scripts/coverage-gate.sh
#
# Usage: coverage-gate.sh <coverage.out> <threshold_percent>
#
# Reads a go cover profile and fails if any package (except cmd/) has
# coverage below the threshold. cmd/ is excluded because main.go is
# largely wiring that provides diminishing returns for unit tests —
# its logic is exercised in integration tests (Wave 2+) instead.

set -euo pipefail

PROFILE="${1:?coverage profile required}"
THRESHOLD="${2:?threshold percent required}"

if [ ! -f "$PROFILE" ]; then
    echo "coverage profile not found: $PROFILE" >&2
    exit 1
fi

# go tool cover -func emits lines like:
#   .../api/push.go:15:  (*PushHandler).ServeHTTP  85.7%
#   total:                                          (statements)       87.3%
#
# We aggregate per-package and compare against the threshold.

go tool cover -func="$PROFILE" | \
    awk -v threshold="$THRESHOLD" '
    # Skip the "total" line at the end.
    /^total:/ { next }

    # Extract package directory and coverage percentage.
    {
        file = $1
        sub(/:.*/, "", file)           # drop :line:col suffix
        # Package = everything before the last /.
        n = split(file, parts, "/")
        pkg = ""
        for (i = 1; i < n; i++) {
            pkg = pkg (i > 1 ? "/" : "") parts[i]
        }
        pct = $NF
        sub(/%$/, "", pct)

        total[pkg] += pct
        count[pkg]++
    }

    END {
        fails = 0
        for (pkg in total) {
            # Exclude cmd/ packages (wiring only).
            # Exclude packages where per-package coverage is not a meaningful signal:
            #   - cmd/           wiring only; integration-tested
            #   - internal/testutil  test infrastructure; coverage attributes to callers
            #   - tests/conformance  parametric suite; Wave 1 exercises one capability shape
            if (pkg ~ /\/cmd(\/|$)/)                continue
            if (pkg ~ /\/internal\/testutil(\/|$)/) continue
            if (pkg ~ /\/tests\/conformance(\/|$)/) continue

            avg = total[pkg] / count[pkg]
            if (avg < threshold+0) {
                printf "FAIL  %-50s  %6.2f%%  (< %d%%)\n", pkg, avg, threshold
                fails++
            } else {
                printf "ok    %-50s  %6.2f%%\n", pkg, avg
            }
        }
        if (fails > 0) exit 1
    }
    '
