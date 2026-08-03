#!/usr/bin/env bash
#
# Fail when total statement coverage drops below a floor.
#
# A floor, not a target. It starts at whatever the first green run measured,
# rounded down, and moves up deliberately. A floor set above current coverage is
# a permanently red build that everyone learns to ignore, which is worse than no
# floor at all.
#
# One total rather than per-package floors: per-package numbers let the domain
# sit at 95% and the adapters at 5% while the headline stays respectable.
set -euo pipefail

profile="${1:-coverage.out}"
floor="${2:-${COVERAGE_FLOOR:-0}}"

if [ ! -s "$profile" ]; then
    echo "error: coverage profile $profile is missing or empty" >&2
    exit 1
fi

total=$(go tool cover -func="$profile" | awk '/^total:/ { sub(/%/, "", $3); print $3 }')

if [ -z "$total" ]; then
    echo "error: could not read a total from $profile" >&2
    exit 1
fi

printf 'total statement coverage: %s%% (floor %s%%)\n' "$total" "$floor"

# awk rather than bash arithmetic: these are decimals.
if awk -v t="$total" -v f="$floor" 'BEGIN { exit (t + 0 >= f + 0) ? 0 : 1 }'; then
    exit 0
fi

if [ -n "${GITHUB_ACTIONS:-}" ]; then
    echo "::error::coverage ${total}% is below the floor of ${floor}%"
fi
cat >&2 <<MSG
error: coverage ${total}% is below the floor of ${floor}%

  Either add tests for what changed, or -- if the drop is deliberate and
  understood -- lower COVERAGE_FLOOR in .github/workflows/ci.yml in the same
  change, so the decision is reviewable rather than silent.
MSG
exit 1
