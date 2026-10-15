#!/usr/bin/env bash
# require-released-limes.test.sh — require-released-limes.sh raises a nested
# module's requirement of limes on its own branch and opens its pull request,
# updates the one still open to a newer release, never a fork's pull request
# from a branch of the same name, leaves alone a module that
# already requires that release or a later one or none at all, refuses a tag
# that is not a release's, and does nothing without a token; and release.yaml
# proposes it for every nested module it releases.
#
# A scratch repository stands in for this one: a stub limes at its root and a
# module requiring it through a local replace, as pgstore does, so go tidies
# it offline. Its origin is a bare repository beside it. A fake gh logs every
# call to CALLS and answers `pr list` by running its --jq over FAKE_OPEN_PRS,
# the open pull requests from that branch.
#
# Runs from `task verify-all` — no Docker, no network.

set -euo pipefail

root="$(git rev-parse --show-toplevel)"
script="$root/.github/scripts/require-released-limes.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

mkdir "$work/bin"
cat >"$work/bin/gh" <<'GH'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$CALLS"
case "$1 $2" in
    "auth setup-git" | "pr create" | "pr edit") ;;
    "pr list")
        filter=""
        while [ $# -gt 0 ]; do
            if [ "$1" = "--jq" ]; then filter="$2"; fi
            shift
        done
        jq -r "$filter" <<<"${FAKE_OPEN_PRS:-[]}"
        ;;
    *) echo "unexpected gh $*" >&2; exit 2 ;;
esac
GH
chmod +x "$work/bin/gh"
export PATH="$work/bin:$PATH" CALLS="$work/calls" REPO=oleg-tkachuk/limes GH_RETRY_DELAY_SECONDS=0
export GOFLAGS=-mod=mod GOPROXY=off GOWORK=off GOTOOLCHAIN=local
export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com

readonly MODULE=nested
readonly LIMES=github.com/oleg-tkachuk/limes
readonly REQUIRED=v0.1.0

# A scratch checkout of main, its origin a bare repository; MODULE requires
# limes at the version given, or not at all when none is.
scratch() {
    rm -rf "$work/origin.git" "$work/repo"
    git init --quiet --bare --initial-branch=main "$work/origin.git"
    git init --quiet --initial-branch=main "$work/repo"
    cd "$work/repo"
    git remote add origin "$work/origin.git"
    printf 'module %s\n\ngo 1.21\n' "$LIMES" >go.mod
    printf 'package limes\n' >limes.go
    mkdir "$MODULE"
    if [ -n "${1:-}" ]; then
        printf 'module example.com/%s\n\ngo 1.21\n\nrequire %s %s\n\nreplace %s => ../\n' "$MODULE" "$LIMES" "$1" "$LIMES" >"$MODULE/go.mod"
        printf 'package %s\n\nimport _ "%s"\n' "$MODULE" "$LIMES" >"$MODULE/$MODULE.go"
    else
        printf 'module example.com/%s\n\ngo 1.21\n' "$MODULE" >"$MODULE/go.mod"
    fi
    touch "$MODULE/go.sum"
    git add -A
    git commit --quiet -m init
    git push --quiet origin main
    : >"$CALLS"
}

# required is the limes version MODULE requires on branch, as origin has it.
required() {
    git -C "$work/repo" show "origin/$1:$MODULE/go.mod" | awk -v m="$LIMES" '$1 == "require" && $2 == m { print $3 }'
}

failures=0
fail() { printf 'FAIL %s\n' "$1"; failures=$((failures + 1)); }
run() { (cd "$work/repo" && GH_TOKEN="${TOKEN-token}" "$script" "$@"); }

# A release past what the module requires: a branch of its own, its
# requirement raised in one commit, and a pull request opened.
scratch "$REQUIRED"
run "$MODULE" v0.2.0 >/dev/null || fail "raising the requirement failed"
git -C "$work/repo" fetch --quiet origin
[ "$(required "require-limes/$MODULE")" = v0.2.0 ] || fail "the branch requires $(required "require-limes/$MODULE"), want v0.2.0"
[ "$(required main)" = "$REQUIRED" ] || fail "main was changed"
[ "$(git -C "$work/repo" log -1 --format=%s "origin/require-limes/$MODULE")" = "build($MODULE): require limes v0.2.0" ] || fail "the commit's subject"
grep -q "^pr create .*--head require-limes/$MODULE --title build($MODULE): require limes v0.2.0" "$CALLS" || fail "no pull request opened: $(cat "$CALLS")"

# A newer release while that pull request is open: the branch moves to it
# and the pull request is retitled, not opened again.
: >"$CALLS"
(cd "$work/repo" && git switch --quiet main)
FAKE_OPEN_PRS='[{"number": 7, "isCrossRepository": false}]' run "$MODULE" v0.3.0 >/dev/null || fail "updating the open pull request failed"
git -C "$work/repo" fetch --quiet origin
[ "$(required "require-limes/$MODULE")" = v0.3.0 ] || fail "the branch requires $(required "require-limes/$MODULE"), want v0.3.0"
grep -q '^pr edit 7 .*--title build('"$MODULE"'): require limes v0.3.0' "$CALLS" || fail "the open pull request not updated: $(cat "$CALLS")"
! grep -q '^pr create' "$CALLS" || fail "a second pull request opened"

# A fork's pull request from a branch of the same name is not ours: a pull
# request of our own is opened beside it, and the fork's is not touched.
scratch "$REQUIRED"
FAKE_OPEN_PRS='[{"number": 9, "isCrossRepository": true}]' run "$MODULE" v0.2.0 >/dev/null || fail "beside a fork's pull request: failed"
! grep -q '^pr edit' "$CALLS" || fail "a fork's pull request was edited: $(cat "$CALLS")"
grep -q "^pr create .*--head require-limes/$MODULE " "$CALLS" || fail "beside a fork's pull request: none opened: $(cat "$CALLS")"
grep -q '^pr list .*--json number,isCrossRepository ' "$CALLS" || fail "pr list did not ask which pull requests are a fork's: $(cat "$CALLS")"

# Nothing to raise: the same release, an older one, or no requirement.
for tc in "$REQUIRED:$REQUIRED" "v0.10.0:v0.9.0" ":v0.2.0"; do
    scratch "${tc%%:*}"
    run "$MODULE" "${tc##*:}" >/dev/null || fail "requiring ${tc%%:*}, release ${tc##*:}: failed"
    [ ! -s "$CALLS" ] || fail "requiring '${tc%%:*}', release ${tc##*:}: gh called: $(cat "$CALLS")"
    [ -z "$(git -C "$work/repo" ls-remote origin "refs/heads/require-limes/*")" ] || fail "requiring '${tc%%:*}', release ${tc##*:}: a branch pushed"
done

# No token: a warning, nothing pushed.
scratch "$REQUIRED"
out="$(TOKEN='' run "$MODULE" v0.2.0)" || fail "a run without a token failed"
[[ "$out" == *"AUTOMERGE_TOKEN is not set"* ]] || fail "a run without a token did not warn: $out"
[ -z "$(git -C "$work/repo" ls-remote origin "refs/heads/require-limes/*")" ] || fail "a run without a token pushed"

# Not a release's tag, or no module.
for args in "$MODULE 0.2.0" "$MODULE v0.2" "$MODULE" "missing v0.2.0"; do
    # shellcheck disable=SC2086 # the arguments are split on purpose
    if run $args >/dev/null 2>&1; then fail "accepted: $args"; fi
done

# release.yaml proposes it for each nested module it releases: every
# <NAME>_MODULE of its env is in the loop that calls this script.
release="$root/.github/workflows/release.yaml"
loop="$(grep -E '^ +for module in .*; do' "$release" || true)"
[ -n "$loop" ] || fail "release.yaml has no loop over the nested modules"
for name in $(grep -oE '^  [A-Z]+_MODULE:' "$release" | tr -d ' :'); do
    [[ "$loop" == *"\"\$$name\""* ]] || fail "release.yaml does not propose \$$name's requirement of limes"
done

if [ "$failures" -gt 0 ]; then
    exit 1
fi
echo "require-released-limes: every case matches"
