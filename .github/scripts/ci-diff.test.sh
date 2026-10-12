#!/usr/bin/env bash
# ci-diff.test.sh — ci-diff.sh classifies the diff a pull request or a push
# names, and runs everything when there is no diff to classify.
#
# Runs from `task verify-all` — no Docker, no network.

set -euo pipefail

script="$(git rev-parse --show-toplevel)/.github/scripts/ci-diff.sh"
work="$(mktemp -d "${TMPDIR:-/tmp}/ci-diff.XXXXXX")"
trap 'rm -rf "$work"' EXIT

cd "$work"
git init -q repo && cd repo
commit() { git add -A && git -c user.name=t -c user.email=t@example.invalid commit -q --allow-empty -m "$1"; }

failures=0
check() { # name, want, got
    if [ "$2" != "$3" ]; then
        printf 'FAIL %s: want %q, got %q\n' "$1" "$2" "$3"
        failures=$((failures + 1))
    fi
}
classify() { env -u BASE -u HEAD -u BEFORE -u AFTER "$@" "$script" 2>/dev/null; }

echo 'package m' >m.go
commit base
base=$(git rev-parse HEAD)
mkdir -p docs && echo note >docs/guide.md
commit docs
docs=$(git rev-parse HEAD)
echo 'package m // more' >m.go
commit code
code=$(git rev-parse HEAD)

check "a pull request with documentation only" "relevant=false" "$(classify BASE="$base" HEAD="$docs")"
check "a pull request with code" "relevant=true" "$(classify BASE="$base" HEAD="$code")"
check "a push of documentation only" "relevant=false" "$(classify BEFORE="$base" AFTER="$docs")"
check "a push of code" "relevant=true" "$(classify BEFORE="$docs" AFTER="$code")"
check "a pull request's ends win over a push's" "relevant=false" "$(classify BASE="$base" HEAD="$docs" BEFORE="$docs" AFTER="$code")"
check "a push that created the branch" "relevant=true" \
    "$(classify BEFORE=0000000000000000000000000000000000000000 AFTER="$docs")"
check "a base this clone does not have" "relevant=true" \
    "$(classify BEFORE=1111111111111111111111111111111111111111 AFTER="$docs")"
check "a run by hand, with no diff" "relevant=true" "$(classify AFTER="$docs")"

if [ "$failures" -gt 0 ]; then
    exit 1
fi
echo "ci-diff: every case matches"
