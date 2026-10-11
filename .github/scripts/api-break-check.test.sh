#!/usr/bin/env bash
# api-break-check.test.sh — api-break-check.sh refuses an unmarked break of
# the API, and passes a marked one or a compatible change.
#
# Runs from `task verify-all` against a scratch module — no network beyond
# the apidiff that tools/go.mod pins.

set -euo pipefail

root="$(git rev-parse --show-toplevel)"
script="$root/.github/scripts/api-break-check.sh"
export APIDIFF="go tool -modfile=$root/tools/go.mod apidiff"

scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT

# A module whose API is one exported function, on its own main.
git -C "$scratch" init --quiet --initial-branch=main
git -C "$scratch" config user.name test
git -C "$scratch" config user.email test@example.com
(cd "$scratch" && go mod init example.com/m >/dev/null 2>&1)
printf 'package m\n\nfunc A() {}\n' >"$scratch/m.go"
git -C "$scratch" add -A
git -C "$scratch" commit --quiet -m "feat: add A"

failures=0
# check name, want (pass|fail), change (drop|add), commit messages...
check() {
    local name="$1" want="$2" change="$3"
    shift 3
    git -C "$scratch" checkout --quiet -B case main
    case "$change" in
        drop) printf 'package m\n' >"$scratch/m.go" ;;
        add) printf 'package m\n\nfunc A() {}\n\nfunc B() {}\n' >"$scratch/m.go" ;;
    esac
    local message
    for message in "$@"; do
        git -C "$scratch" commit --quiet --allow-empty -a -m "$message"
    done
    local got=pass
    (cd "$scratch" && "$script" main case >/dev/null 2>&1) || got=fail
    if [ "$want" != "$got" ]; then
        printf 'FAIL %s: want %s, got %s\n' "$name" "$want" "$got"
        failures=$((failures + 1))
    fi
}

check "an unmarked break" fail drop "fix: drop A"
check "a break marked in the subject" pass drop "fix!: drop A"
check "a break marked with a scope" pass drop "refactor(m)!: drop A"
check "a break marked in a footer" pass drop "$(printf 'fix: drop A\n\nBREAKING CHANGE: A is gone')"
check "a break marked by a later commit" pass drop "refactor: drop A" "docs!: say A is gone"
check "a ! that is not in the type" fail drop "fix: drop A!: really"
check "a compatible addition" pass add "feat: add B"
check "a marked change with no break" pass add "feat!: change the wire format"

# The check leaves no worktree behind.
if [ "$(git -C "$scratch" worktree list | wc -l)" -ne 1 ]; then
    printf 'FAIL worktrees left behind:\n%s\n' "$(git -C "$scratch" worktree list)"
    failures=$((failures + 1))
fi

if [ "$failures" -gt 0 ]; then
    exit 1
fi
echo "api-break-check: every case matches"
