#!/usr/bin/env bash
# check-range.test.sh — check-range.sh checks a pull request from its base
# and a push from the newest release tag below it, or from nothing before
# any release.
#
# Runs from `task verify-all` against a scratch repository — no network.

set -euo pipefail

root="$(git rev-parse --show-toplevel)"
script="$root/.github/scripts/check-range.sh"

scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT

git -C "$scratch" init --quiet --initial-branch=main
git -C "$scratch" config user.name test
git -C "$scratch" config user.email test@example.com
commit() { git -C "$scratch" commit --quiet --allow-empty -m "$1" && git -C "$scratch" rev-parse HEAD; }

failures=0
expect() { # name, want, args...
    local name="$1" want="$2"
    shift 2
    local got
    got="$(cd "$scratch" && "$script" "$@")"
    if [ "$got" != "$want" ]; then
        printf 'FAIL %s: want %q, got %q\n' "$name" "$want" "$got"
        failures=$((failures + 1))
    fi
}

first="$(commit "feat: start")"
expect "a push before any release" "$(printf 'base=\nhead=%s' "$first")" push "" "" "$first"
git -C "$scratch" tag v0.1.0
git -C "$scratch" tag v0.2.0
later="$(commit "fix: later")"
expect "a push after a release" "$(printf 'base=v0.2.0\nhead=%s' "$later")" push "" "" "$later"
expect "a pull request" "$(printf 'base=%s\nhead=%s' "$first" "$later")" pull_request "$first" "$later" "$later"

# With the module table, a push is checked from the module's own newest tag.
mkdir -p "$scratch/.github/release"
cp "$root/.github/release/modules.cjs" "$scratch/.github/release/"
git -C "$scratch" add .github
git -C "$scratch" commit --quiet -m "build: add the module table"
git -C "$scratch" tag redisreplay/v0.1.0
nested_later="$(commit "fix(redisreplay): later")"
expect "a push, the root module" "$(printf 'base=v0.2.0\nhead=%s' "$nested_later")" push "" "" "$nested_later"
expect "a push, the nested module" "$(printf 'base=redisreplay/v0.1.0\nhead=%s' "$nested_later")" push "" "" "$nested_later" redisreplay
expect "a pull request, the nested module" "$(printf 'base=%s\nhead=%s' "$first" "$nested_later")" pull_request "$first" "$nested_later" "$nested_later" redisreplay

if [ "$failures" -gt 0 ]; then
    exit 1
fi
echo "check-range: every case matches"
