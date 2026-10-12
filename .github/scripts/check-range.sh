#!/usr/bin/env bash
# check-range.sh — the commits the marker and API checks read.
#
# Usage: check-range.sh <event> <pr-base> <pr-head> <sha>
#
# Prints base= and head= lines for $GITHUB_OUTPUT. A pull request is checked
# from its base. A push to main is checked from the newest release tag below
# it: a push that reached main past the pull request rule — an admin's —
# carries commits no pull request check read, and they must not reach a
# release unchecked. base is empty before any release: nothing to check from.

set -euo pipefail

# shellcheck source=.github/scripts/breaking-markers.sh
source "$(dirname "${BASH_SOURCE[0]}")/breaking-markers.sh"

readonly PULL_REQUEST_EVENT=pull_request

if [ "$#" -ne 4 ]; then
    echo "usage: $0 <event> <pr-base> <pr-head> <sha>" >&2
    exit 2
fi
event="$1" pr_base="$2" pr_head="$3" sha="$4"

if [ "$event" = "$PULL_REQUEST_EVENT" ]; then
    printf 'base=%s\nhead=%s\n' "$pr_base" "$pr_head"
    exit 0
fi
printf 'base=%s\nhead=%s\n' "$(release_line "$sha")" "$sha"
