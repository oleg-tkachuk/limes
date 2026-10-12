#!/usr/bin/env bash
# release-catch-up.test.sh — release-catch-up.sh fast-forwards to main's head
# and releases when what landed meanwhile is documentation, and not when it
# is code.
#
# A bare repository stands in for origin. Runs from `task verify-all` — no
# Docker, no network.

set -euo pipefail

script="$(git rev-parse --show-toplevel)/.github/scripts/release-catch-up.sh"
work="$(mktemp -d "${TMPDIR:-/tmp}/release-catch-up.XXXXXX")"
trap 'rm -rf "$work"' EXIT

git init -q --bare --initial-branch=main "$work/origin.git"
git clone -q "$work/origin.git" "$work/pusher" 2>/dev/null
commit() { # dir, message
    git -C "$1" add -A
    git -C "$1" -c user.name=t -c user.email=t@example.invalid commit -q --allow-empty -m "$2"
}
echo 'package m' >"$work/pusher/m.go"
commit "$work/pusher" "feat: code"
git -C "$work/pusher" push -q origin main
git clone -q "$work/origin.git" "$work/release"
dispatched=$(git -C "$work/release" rev-parse HEAD)

failures=0
check() { # name, want
    local got
    got="$(cd "$work/release" && "$script" "$dispatched" 2>/dev/null)"
    if [ "$2" != "$got" ]; then
        printf 'FAIL %s: want %q, got %q\n' "$1" "$2" "$got"
        failures=$((failures + 1))
    fi
}

check "main where the checkout left it" "release=true"

mkdir -p "$work/pusher/docs" && echo note >"$work/pusher/docs/guide.md"
commit "$work/pusher" "docs: a note"
git -C "$work/pusher" push -q origin main
check "documentation landed after the checkout" "release=true"
if [ "$(git -C "$work/release" rev-parse HEAD)" != "$(git -C "$work/pusher" rev-parse HEAD)" ]; then
    printf 'FAIL the checkout was not brought up to main\n'
    failures=$((failures + 1))
fi

echo 'package m // more' >"$work/pusher/m.go"
commit "$work/pusher" "feat: more code"
git -C "$work/pusher" push -q origin main
check "code landed after the checkout" "release=false"

if [ "$failures" -gt 0 ]; then
    exit 1
fi
echo "release-catch-up: every case matches"
