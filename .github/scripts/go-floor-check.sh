#!/usr/bin/env bash
# go-floor-check.sh — is the oldest Go CI tests on a patch of go.mod's line?
#
# Usage: go-floor-check.sh <GO_FLOOR_VERSION> <go.mod directive>
#
# Both name a Go release line by their first two parts — 1.26 of 1.26.9 and
# of 1.26.0, or of a directive written 1.26 — so the floor is a patch of the
# directive exactly when those agree. A directive may carry two parts or
# three; comparing what is left after the last dot would read "1.26" as 1.

set -euo pipefail

if [ "$#" -ne 2 ]; then
    echo "usage: $0 <floor> <directive>" >&2
    exit 2
fi
floor="$1"
directive="$2"

# line is a version's first two dot-separated parts.
line() { cut -d. -f1,2 <<<"$1"; }

if [ "$(line "$floor")" != "$(line "$directive")" ]; then
    printf 'GO_FLOOR_VERSION %s is not a patch of the go.mod directive %s\n' "$floor" "$directive" >&2
    exit 1
fi
