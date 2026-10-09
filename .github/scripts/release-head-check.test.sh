#!/usr/bin/env bash
# release-head-check.test.sh — release-head-check.sh releases only the commit
# CI passed.
#
# The case that matters is a head that moved: a pull request merged while the
# dispatching CI run finished. Reporting it as current tags a commit whose own
# checks have not run.
#
# Runs from `task verify-all` — no Docker, no network.

set -euo pipefail

script="$(git rev-parse --show-toplevel)/.github/scripts/release-head-check.sh"
work="$(mktemp -d "${TMPDIR:-/tmp}/release-head-check.XXXXXX")"
trap 'rm -rf "$work"' EXIT

cd "$work"
git init -q repo && cd repo
commit() { git -c user.name=t -c user.email=t@example.invalid commit -q --allow-empty -m "$1"; }

failures=0
check() { # name, want, got
    if [ "$2" != "$3" ]; then
        printf 'FAIL %s: want %q, got %q\n' "$1" "$2" "$3"
        failures=$((failures + 1))
    fi
}

commit one
passed=$(git rev-parse HEAD)
check "the head CI passed" "release=true" "$("$script" "$passed")"
check "the head by its short sha" "release=true" "$("$script" "${passed:0:8}")"

commit two
check "main moved past it" "release=false" "$("$script" "$passed" 2>/dev/null)"
check "a sha this clone does not have" "release=false" "$("$script" 0000000000000000000000000000000000000000 2>/dev/null)"

if "$script" "" >/dev/null 2>&1; then
    printf 'FAIL %s\n' "an empty sha is accepted"
    failures=$((failures + 1))
fi

if [ "$failures" -gt 0 ]; then
    exit 1
fi
printf '%s\n' "release head check: releases only the commit CI passed"
