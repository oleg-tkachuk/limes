#!/usr/bin/env bash
# api-break-check.test.sh — api-break-check.sh refuses an unmarked break of
# the API, and passes a marked one or a compatible change; from v1.0.0 on it
# refuses every break. A command's changes are no break.
#
# Runs from `task verify-all` against a scratch module — no network beyond
# the apidiff that tools/go.mod pins.

set -euo pipefail

root="$(git rev-parse --show-toplevel)"
script="$root/.github/scripts/api-break-check.sh"
export APIDIFF="go tool -modfile=$root/tools/go.mod apidiff"

scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT

# A module whose API is one exported function, on its own main, beside a
# command that declares an exported function of its own.
git -C "$scratch" init --quiet --initial-branch=main
git -C "$scratch" config user.name test
git -C "$scratch" config user.email test@example.com
(cd "$scratch" && go mod init example.com/m >/dev/null 2>&1)
printf 'package m\n\nfunc A() {}\n' >"$scratch/m.go"
mkdir "$scratch/cmd"
printf 'package main\n\nfunc Exported() {}\n\nfunc main() {}\n' >"$scratch/cmd/main.go"
git -C "$scratch" add -A
git -C "$scratch" commit --quiet -m "feat: add A"

failures=0
# check name, want (pass|fail), change, commit messages... The change is
# drop (A), add (B), dropcmd (the command), cmdexport (the command's
# Exported) or dropboth (A and the command).
check() {
    local name="$1" want="$2" change="$3"
    shift 3
    git -C "$scratch" checkout --quiet -B case main
    case "$change" in
        drop) printf 'package m\n' >"$scratch/m.go" ;;
        add) printf 'package m\n\nfunc A() {}\n\nfunc B() {}\n' >"$scratch/m.go" ;;
        dropcmd) git -C "$scratch" rm --quiet -r cmd ;;
        cmdexport) printf 'package main\n\nfunc main() {}\n' >"$scratch/cmd/main.go" ;;
        dropboth)
            printf 'package m\n' >"$scratch/m.go"
            git -C "$scratch" rm --quiet -r cmd
            ;;
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
check "a break marked by an earlier commit" pass drop "fix!: drop A" "docs: say A is gone"
check "a ! that is not in the type" fail drop "fix: drop A!: really"
check "a break marked in a hyphenated footer" pass drop "$(printf 'fix: drop A\n\nBREAKING-CHANGE: A is gone')"
check "a marker in the body, not the subject" fail drop "$(printf 'fix: drop A\n\nfix!: is not a subject')"
# Spellings the release's commit parser reads as breaking.
check "a capitalised type" pass drop "Refactor!: drop A"
check "a type with an underscore" pass drop "feat_x!: drop A"
check "a footer in another case" pass drop "$(printf 'fix: drop A\n\nBreaking change: A is gone')"
check "a footer in a list" pass drop "$(printf 'fix: drop A\n\n* BREAKING CHANGE: A is gone')"
check "a footer with no colon" pass drop "$(printf 'fix: drop A\n\nBREAKING-CHANGE A is gone')"
check "a scope with a parenthesis in it" pass drop "feat(a)b)!: drop A"
check "two scopes" pass drop "feat(a)(b)!: drop A"
check "a type that is not ASCII" fail drop "féat!: drop A"
check "a footer as the subject" fail drop "BREAKING CHANGE: drop A"
check "a compatible addition" pass add "feat: add B"
check "a marked change with no break" pass add "feat!: change the wire format"
check "a command removed" pass dropcmd "docs: move the command"
check "a command's exported function removed" pass cmdexport "docs: trim the command"
check "a command removed beside an unmarked break" fail dropboth "docs: drop A and the command"

# From v1.0.0 on, no break passes, marked or not.
git -C "$scratch" tag v1.0.0 main
check "on v1, an unmarked break" fail drop "fix: drop A"
check "on v1, a marked break" fail drop "fix!: drop A"
check "on v1, a compatible addition" pass add "feat: add B"

# The check leaves no worktree behind.
if [ "$(git -C "$scratch" worktree list | wc -l)" -ne 1 ]; then
    printf 'FAIL worktrees left behind:\n%s\n' "$(git -C "$scratch" worktree list)"
    failures=$((failures + 1))
fi

# A repository of two modules, as the release scopes them: each module's API
# is checked in its own directory, against the markers of the commits that
# touch it.
nested="$scratch/nested"
readonly NESTED=redisreplay
git -C "$scratch" init --quiet --initial-branch=main "$nested"
git -C "$nested" config user.name test
git -C "$nested" config user.email test@example.com
mkdir -p "$nested/.github/release"
cp "$root/.github/release/modules.cjs" "$nested/.github/release/"
(cd "$nested" && go mod init example.com/m >/dev/null 2>&1)
printf 'package m\n\nfunc A() {}\n' >"$nested/m.go"
git -C "$nested" add -A
git -C "$nested" commit --quiet -m "feat: add A"
git -C "$nested" checkout --quiet -b without-nested
mkdir "$nested/$NESTED"
(cd "$nested/$NESTED" && go mod init "example.com/m/$NESTED" >/dev/null 2>&1)
printf 'package %s\n\nfunc R() {}\n' "$NESTED" >"$nested/$NESTED/r.go"
git -C "$nested" add -A
git -C "$nested" commit --quiet -m "feat($NESTED): add R"
git -C "$nested" checkout --quiet main
git -C "$nested" merge --quiet --ff-only without-nested

# check_module name, want, module, messages: the first commit drops R, each
# message after it is a commit touching the root module only.
check_module() {
    local name="$1" want="$2" module="$3" first="$4"
    shift 4
    git -C "$nested" checkout --quiet -B case main
    printf 'package %s\n' "$NESTED" >"$nested/$NESTED/r.go"
    git -C "$nested" commit --quiet -a -m "$first"
    local message
    for message in "$@"; do
        printf '// %s\n' "$message" >>"$nested/m.go"
        git -C "$nested" commit --quiet -a -m "$message"
    done
    local got=pass
    (cd "$nested" && "$script" main case "$module" >/dev/null 2>&1) || got=fail
    if [ "$want" != "$got" ]; then
        printf 'FAIL %s: want %s, got %s\n' "$name" "$want" "$got"
        failures=$((failures + 1))
    fi
}

check_module "a nested break, unmarked" fail "$NESTED" "fix($NESTED): drop R"
check_module "a nested break, marked on its commit" pass "$NESTED" "fix($NESTED)!: drop R"
check_module "a nested break, marked on a root commit only" fail "$NESTED" "fix($NESTED): drop R" "docs!: say R is gone"
check_module "a nested break, checked for the root" pass limes "fix($NESTED): drop R"

# check_root name, want, message: one commit moves A out of the root module
# into the nested one, so it touches both.
check_root() {
    local name="$1" want="$2" message="$3"
    git -C "$nested" checkout --quiet -B case main
    printf 'package m\n' >"$nested/m.go"
    printf 'package %s\n\nfunc R() {}\n\nfunc A() {}\n' "$NESTED" >"$nested/$NESTED/r.go"
    git -C "$nested" commit --quiet -a -m "$message"
    local got=pass
    (cd "$nested" && "$script" main case limes >/dev/null 2>&1) || got=fail
    if [ "$want" != "$got" ]; then
        printf 'FAIL %s: want %s, got %s\n' "$name" "$want" "$got"
        failures=$((failures + 1))
    fi
}

check_root "a root break, marked for the root" pass "refactor!: move A to $NESTED"
# The root module's rules release nothing of a commit scoped to a nested
# module, breaking or not, so its marker is none of the root's.
check_root "a root break, marked only under the nested scope" fail "refactor($NESTED)!: move A to $NESTED"
check_root "a root break, a nested-scoped footer" fail "$(printf 'refactor(%s): move A\n\nBREAKING CHANGE: A moved' "$NESTED")"

# A module the merge base does not hold yet has no API to break.
git -C "$nested" checkout --quiet -B case without-nested
git -C "$nested" checkout --quiet -B base main~1
got=pass
(cd "$nested" && "$script" base case "$NESTED" >/dev/null 2>&1) || got=fail
if [ "$got" != pass ]; then
    printf 'FAIL a module new since the merge base: want pass, got %s\n' "$got"
    failures=$((failures + 1))
fi

if [ "$failures" -gt 0 ]; then
    exit 1
fi
echo "api-break-check: every case matches"
