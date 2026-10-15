#!/usr/bin/env bash
# queue-auto-merge.test.sh — queue-auto-merge.sh queues a pull request whose
# changes leave CI as it is, leaves one that changes what CI runs, takes one
# off the queue when a later push makes it change what CI runs, never queues
# again on a push, fails
# rather than queue one whose diff it cannot read, and does nothing without a
# token.
#
# A fake gh stands in for the API. It logs every call's arguments to CALLS,
# answers `pr diff` with FAKE_FILES, and fails it when FAKE_DIFF_FAILS is set;
# `pr view` says auto-merge is armed when FAKE_QUEUED is true.
#
# Runs from `task verify-all` — no Docker, no network.

set -euo pipefail

root="$(git rev-parse --show-toplevel)"
script="$root/.github/scripts/queue-auto-merge.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

cat >"$work/gh" <<'GH'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$CALLS"
case "$1 $2" in
    "pr diff")
        [ -z "${FAKE_DIFF_FAILS:-}" ] || { echo "HTTP 406: diff too large" >&2; exit 1; }
        printf '%s' "$FAKE_FILES"
        ;;
    "pr view") printf '%s\n' "${FAKE_QUEUED:-false}" ;;
    "pr merge") ;;
    *) echo "unexpected gh $*" >&2; exit 2 ;;
esac
GH
chmod +x "$work/gh"
export PATH="$work:$PATH" CALLS="$work/calls" REPO=oleg-tkachuk/limes PR=7 GH_TOKEN=token ACTION=opened
export GH_RETRY_DELAY_SECONDS=0

failures=0
fail() { printf 'FAIL %s\n' "$1"; failures=$((failures + 1)); }

queued() { grep -Eq -- '^pr merge 7 .*--auto( |$)' "$CALLS"; }
dequeued() { grep -q -- '^pr merge 7 .*--disable-auto' "$CALLS"; }

check() { # name, want queued (true/false), changed paths
    : >"$CALLS"
    FAKE_FILES="$3" "$script" >/dev/null || { fail "$1: the script failed"; return; }
    if [ "$2" = true ] && ! queued; then fail "$1: not queued"; fi
    if [ "$2" = false ] && queued; then fail "$1: queued"; fi
}

check "Go source" true "verifier.go"
check "documentation" true "$(printf 'README.md\ndocs/guide.md')"
check "a nested module" true "pgstore/store.go"
check "no change" true ""
check "a workflow" false ".github/workflows/ci.yaml"
check "a workflow script" false ".github/scripts/ci-relevant.sh"
check "the release tooling" false ".github/release/modules.cjs"
check "the tasks" false "Taskfile.yaml"
check "a Taskfile Task prefers" false "Taskfile.yml"
check "a lowercase Taskfile" false "taskfile.yaml"
check "a dist Taskfile" false "Taskfile.dist.yaml"
check "the linter's configuration" false ".golangci.yml"
check "another spelling of it" false ".golangci.yaml"
check "a pinned tool" false "tools/go.mod"
check "code beside a workflow" false "$(printf 'verifier.go\n.github/workflows/ci.yaml')"
check "a name that only starts like one" true "github/notes.md"

push() { # name, FAKE_QUEUED, want dequeued (true/false), changed paths
    : >"$CALLS"
    ACTION=synchronize FAKE_QUEUED="$2" FAKE_FILES="$4" "$script" >/dev/null || { fail "$1: the script failed"; return; }
    queued && fail "$1: queued again on a push"
    if [ "$3" = true ] && ! dequeued; then fail "$1: left on the queue"; fi
    if [ "$3" = false ] && dequeued; then fail "$1: taken off the queue"; fi
}

push "a push of Go source to a queued one" true false "verifier.go"
push "a push of Go source to a held one" false false "verifier.go"
push "a push of a workflow to a queued one" true true "$(printf 'verifier.go\n.github/workflows/ci.yaml')"
push "a push of a workflow to an unqueued one" false false ".github/workflows/ci.yaml"
push "a push of a Taskfile to a queued one" true true "Taskfile.yml"

: >"$CALLS"
if FAKE_DIFF_FAILS=1 FAKE_FILES="" "$script" >/dev/null 2>&1; then
    fail "an unreadable diff: the script succeeded"
fi
queued && fail "an unreadable diff: queued"

: >"$CALLS"
GH_TOKEN="" FAKE_FILES="verifier.go" "$script" >/dev/null
[ -s "$CALLS" ] && fail "no token: gh was called"

if [ "$failures" -gt 0 ]; then
    exit 1
fi
echo "queue-auto-merge: every case matches"
