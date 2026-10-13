#!/usr/bin/env bash
# check-modules.sh — run a release check over every module the repository
# releases.
#
# Usage: check-modules.sh <api|markers> <event> <pr-base> <pr-head> <sha>
#
# For each module in .github/release/modules.cjs, names the commits to check
# (check-range.sh) and runs api-break-check.sh or marker-check.sh over them.
# A module with nothing to check from — a push before its first release — is
# skipped. Every module is checked, and the run fails when any check did.

set -euo pipefail

scripts="$(dirname "${BASH_SOURCE[0]}")"
# shellcheck source=.github/scripts/breaking-markers.sh
source "$scripts/breaking-markers.sh"

readonly USAGE="usage: $0 <api|markers> <event> <pr-base> <pr-head> <sha>"
if [ "$#" -ne 5 ]; then
    echo "$USAGE" >&2
    exit 2
fi
check="$1" event="$2" pr_base="$3" pr_head="$4" sha="$5"
case "$check" in
api) script="$scripts/api-break-check.sh" ;;
markers) script="$scripts/marker-check.sh" ;;
*)
    echo "$USAGE" >&2
    exit 2
    ;;
esac

failed=0
while read -r module; do
    # Each check reads /dev/null, never the list of modules this loop reads.
    range="$("$scripts/check-range.sh" "$event" "$pr_base" "$pr_head" "$sha" "$module" </dev/null)"
    base="$(sed -n 's/^base=//p' <<<"$range")"
    head="$(sed -n 's/^head=//p' <<<"$range")"
    if [ -z "$base" ]; then
        printf '%s\n' "$module: no release to check from yet"
        continue
    fi
    "$script" "$base" "$head" "$module" </dev/null || failed=1
done < <(released_modules)

exit "$failed"
