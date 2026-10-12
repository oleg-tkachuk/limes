#!/usr/bin/env bash
# go-floor-check.test.sh — go-floor-check.sh compares release lines, whether
# the directive is written with two parts or three.
#
# Runs from `task verify-all` — no Docker, no network.

set -euo pipefail

script="$(git rev-parse --show-toplevel)/.github/scripts/go-floor-check.sh"

failures=0
check() { # name, want (pass|fail), floor, directive
    local got=pass
    "$script" "$3" "$4" >/dev/null 2>&1 || got=fail
    if [ "$2" != "$got" ]; then
        printf 'FAIL %s: want %s, got %s\n' "$1" "$2" "$got"
        failures=$((failures + 1))
    fi
}

check "a patch of a three-part directive" pass 1.26.9 1.26.0
check "a patch of a two-part directive" pass 1.26.9 1.26
check "another line, three parts" fail 1.27.1 1.26.0
check "another line, two parts" fail 1.25.9 1.26
check "a minor that only shares a prefix" fail 1.2.6 1.26

if [ "$failures" -gt 0 ]; then
    exit 1
fi
echo "go-floor-check: every case matches"
