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
    (cd "$scratch" && PR_TITLE="${TITLE:-}" "$script" main case >/dev/null 2>&1) || got=fail
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

# A title that reads as a footer in the merge commit is refused on any line.
TITLE="Breaking change: drop X" check "on v0, a footer as the title" fail "docs: explain"
TITLE="* BREAKING CHANGE: drop X" check "on v0, a listed footer as the title" fail "docs: explain"
TITLE="docs: explain the breaking change" check "on v0, the words in a title" pass "docs: explain"

# The release config decides before the tag does: with the pre-1.0 rule a
# marker cuts a minor; once the change cutting v1.0.0 removed it, a change
# built on that commit is on the stable line, though v1.0.0 is not tagged yet.
git -C "$scratch" checkout --quiet main
config="$scratch/.github/release/release.config.cjs"
mkdir -p "$(dirname "$config")"
printf 'const BREAKING_IS_A_MINOR_BEFORE_1_0 = {};\n' >"$config"
git -C "$scratch" add .github
git -C "$scratch" commit --quiet -m "build: release breaks as minors"
check "on v0, the pre-1.0 rule in force" pass "feat!: change"
git -C "$scratch" checkout --quiet main
printf 'module.exports = {};\n' >"$config"
git -C "$scratch" commit --quiet -am "build!: release breaks as majors"
check "the rule gone, v1.0.0 not tagged yet" fail "feat!: change"
check "the rule gone, no marker" pass "feat: add"

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
