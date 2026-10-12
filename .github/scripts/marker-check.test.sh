#!/usr/bin/env bash
# marker-check.test.sh — marker-check.sh lets breaking markers through before
# v1.0.0 and refuses every one from then on, a documentation-only change's
# included, in every spelling the release's commit parser reads.
#
# Runs from `task verify-all` against a scratch repository — no network.

set -euo pipefail

root="$(git rev-parse --show-toplevel)"
script="$root/.github/scripts/marker-check.sh"

scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT

git -C "$scratch" init --quiet --initial-branch=main
git -C "$scratch" config user.name test
git -C "$scratch" config user.email test@example.com
git -C "$scratch" commit --quiet --allow-empty -m "feat: start"

failures=0
# check name, want (pass|fail), commit messages...
check() {
    local name="$1" want="$2"
    shift 2
    git -C "$scratch" checkout --quiet -B case main
    local message
    for message in "$@"; do
        printf '%s\n' "$message" >>"$scratch/README.md"
        git -C "$scratch" add README.md
        git -C "$scratch" commit --quiet -m "$message"
    done
    local got=pass
    (cd "$scratch" && "$script" main case >/dev/null 2>&1) || got=fail
    if [ "$want" != "$got" ]; then
        printf 'FAIL %s: want %s, got %s\n' "$name" "$want" "$got"
        failures=$((failures + 1))
    fi
}

# Before any release, and on 0.x, a marker cuts a minor: it passes.
check "no release, a marker" pass "feat!: change"
git -C "$scratch" tag v0.9.0 main
check "on v0, a marker" pass "feat!: change"
check "on v0, no marker" pass "docs: explain"

# From v1.0.0 on, every marker the parser reads is refused.
git -C "$scratch" tag v1.0.0 main
check "on v1, no marker" pass "docs: explain" "feat: add"
check "on v1, a subject marker" fail "feat!: change"
check "on v1, a scoped marker" fail "fix(api)!: change"
check "on v1, a capitalised type" fail "Docs!: change"
check "on v1, a footer" fail "$(printf 'docs: explain\n\nBREAKING CHANGE: gone')"
check "on v1, a footer in another case" fail "$(printf 'docs: explain\n\nbreaking change: gone')"
check "on v1, a marker in an earlier commit" fail "docs!: explain" "docs: more"
check "on v1, a ! outside the type" pass "fix: a!: b"

if [ "$failures" -gt 0 ]; then
    exit 1
fi
echo "marker-check: every case matches"
