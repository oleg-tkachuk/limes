#!/usr/bin/env bash
# merge-green.test.sh — merge-green.sh merges a green pull request at the head
# its run tested, and leaves to a person a draft, a fork's, Renovate's, one
# into another branch, one whose author cannot push, one that changes what CI
# runs, and one that moved past the tested commit; it does nothing without a
# token.
#
# A fake gh stands in for the API. It logs every call's arguments to CALLS;
# `pr view` answers from the FAKE_* variables, the files API with FAKE_FILES —
# one path a line, or old>new for a rename — shaped as GitHub answers and
# put through the script's own --jq,
# a collaborator's permission from FAKE_PERMISSIONS keyed by login (a login
# missing there fails), and `pr merge` fails when FAKE_MERGE_FAILS is set.
#
# Runs from `task verify-all` — no Docker, no network.

set -euo pipefail

root="$(git rev-parse --show-toplevel)"
script="$root/.github/scripts/merge-green.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

cat >"$work/gh" <<'GH'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$CALLS"
case "$1 $2" in
    "pr view")
        changed="${FAKE_CHANGED:-$(printf '%s\n' "$FAKE_FILES" | grep -c .)}"
        printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$FAKE_STATE" "$FAKE_DRAFT" "$FAKE_CROSS" "$FAKE_AUTHOR" "$FAKE_BASE" "$FAKE_HEAD" "$changed"
        ;;
    "pr merge")
        [ -z "${FAKE_MERGE_FAILS:-}" ] || { echo "GraphQL: Head branch was modified" >&2; exit 1; }
        ;;
    "api repos/"*/files)
        while [ "$1" != --jq ]; do shift; done
        printf '%s\n' "$FAKE_FILES" | jq -R -s '
            split("\n") | map(select(. != "") | split(">")
                | if length == 2 then {filename: .[1], previous_filename: .[0], status: "renamed"}
                  else {filename: .[0], status: "modified"} end)' | jq -r "$2"
        ;;
    "api repos/"*/permission)
        login="${2%/permission}"
        login="${login##*/}"
        jq -e --arg l "$login" 'has($l)' <<<"$FAKE_PERMISSIONS" >/dev/null || { echo "HTTP 404: Not Found" >&2; exit 1; }
        jq -r --arg l "$login" '.[$l]' <<<"$FAKE_PERMISSIONS"
        ;;
    *) echo "unexpected gh $*" >&2; exit 2 ;;
esac
GH
chmod +x "$work/gh"

readonly SHA=0123456789abcdef0123456789abcdef01234567
readonly OTHER_SHA=fedcba9876543210fedcba9876543210fedcba98
export PATH="$work:$PATH" CALLS="$work/calls" REPO=oleg-tkachuk/limes GH_TOKEN=token GH_RETRY_DELAY_SECONDS=0
export HEAD_SHA="$SHA" PULL_REQUESTS='[{"number": 7}]'
export FAKE_PERMISSIONS='{"someone": "write", "keeper": "admin", "reader": "read"}'

failures=0
fail() { printf 'FAIL %s\n' "$1"; failures=$((failures + 1)); }

merged() { grep -q -- "^pr merge 7 --repo $REPO --merge --match-head-commit $SHA\$" "$CALLS"; }

# check runs the script on one pull request as the variables after the name
# set it — defaults: open, ready, same-repository, by someone who can push,
# into main, at the tested commit, changing Go source — and wants it merged
# or not.
check() { # name, want merged (true/false), VAR=value...
    local name=$1 want=$2
    shift 2
    : >"$CALLS"
    env FAKE_STATE=OPEN FAKE_DRAFT=false FAKE_CROSS=false FAKE_AUTHOR=someone FAKE_BASE=main FAKE_HEAD="$SHA" \
        FAKE_FILES="verifier.go" "$@" "$script" >/dev/null || { fail "$name: the script failed"; return; }
    if [ "$want" = true ] && ! merged; then fail "$name: not merged"; fi
    if [ "$want" = false ] && merged; then fail "$name: merged"; fi
}

check "Go source by a writer" true
check "documentation" true FAKE_FILES="$(printf 'README.md\ndocs/guide.md')"
check "an admin's" true FAKE_AUTHOR=keeper
check "a nested module's definition" true FAKE_FILES="$(printf 'pgstore/go.mod\npgstore/go.sum')"
check "a draft" false FAKE_DRAFT=true
check "a fork's" false FAKE_CROSS=true
check "a closed one" false FAKE_STATE=CLOSED
check "Renovate's" false FAKE_AUTHOR=app/renovate
check "into another branch" false FAKE_BASE=release/v1
check "by someone who can only read" false FAKE_AUTHOR=reader
check "by someone the API does not know" false FAKE_AUTHOR=stranger
check "moved past the tested commit" false FAKE_HEAD="$OTHER_SHA"
check "a workflow" false FAKE_FILES=".github/workflows/ci.yaml"
check "code beside a workflow" false FAKE_FILES="$(printf 'verifier.go\n.github/workflows/ci.yaml')"
check "a Taskfile Task prefers" false FAKE_FILES="Taskfile.yml"
check "the linter's configuration" false FAKE_FILES=".golangci.yaml"
check "a pinned tool" false FAKE_FILES="tools/golangci-lint/go.mod"
check "the module definition" false FAKE_FILES="go.mod"
check "a workspace" false FAKE_FILES="go.work"
check "a workspace's checksums" false FAKE_FILES="go.work.sum"
check "Task's own settings" false FAKE_FILES=".taskrc.yml"
check "Renovate's settings" false FAKE_FILES="renovate.json"
check "Renovate's settings, another spelling" false FAKE_FILES=".renovaterc.json5"
# A rename names the path it leaves too: moving a CI definition out of its
# place changes what CI runs.
check "a workflow renamed out of .github" false FAKE_FILES="$(printf '.github/workflows/codeql.yaml>docs/codeql.yaml')"
check "CODEOWNERS renamed away" false FAKE_FILES="$(printf '.github/CODEOWNERS>docs/CODEOWNERS')"
check "Renovate's settings renamed away" false FAKE_FILES="$(printf 'renovate.json>docs/renovate.json')"
check "a Go file renamed" true FAKE_FILES="$(printf 'old.go>new.go')"
check "a workspace in a nested module" false FAKE_FILES="pgstore/go.work"
check "a vendor directory" false FAKE_FILES="vendor/modules.txt"
check "Task's cache of a remote include" false FAKE_FILES=".task/remote/taskfile.yaml"
check "a nested vendor directory" false FAKE_FILES="pgstore/vendor/modules.txt"
# A count of changed files GitHub did not give is not a complete listing.
check "a count that is not one" false FAKE_CHANGED=null
# A file the files API did not list could be a CI definition.
check "more files changed than listed" false FAKE_FILES="verifier.go" FAKE_CHANGED=2
# A refused merge — the head moved between the read and the merge — is
# reported and leaves the pull request as it is.
check "a merge GitHub refuses" true FAKE_MERGE_FAILS=1

# Every pull request of the run is decided.
: >"$CALLS"
env PULL_REQUESTS='[{"number": 7}, {"number": 8}]' FAKE_STATE=OPEN FAKE_DRAFT=false FAKE_CROSS=false FAKE_AUTHOR=someone \
    FAKE_BASE=main FAKE_HEAD="$SHA" FAKE_FILES="verifier.go" "$script" >/dev/null || fail "two pull requests: the script failed"
[ "$(grep -c '^pr merge' "$CALLS")" = 2 ] || fail "two pull requests: want both merged"

# Inputs that are not what the workflow passes are refused, not used.
for bad in "HEAD_SHA=main" "PULL_REQUESTS=[{\"number\": \"7; rm -rf /\"}]"; do
    if env "$bad" FAKE_STATE=OPEN FAKE_DRAFT=false FAKE_CROSS=false FAKE_AUTHOR=someone FAKE_BASE=main FAKE_HEAD="$SHA" \
        FAKE_FILES="verifier.go" "$script" >/dev/null 2>&1; then
        fail "accepted $bad"
    fi
done

: >"$CALLS"
GH_TOKEN="" "$script" >/dev/null
[ -s "$CALLS" ] && fail "no token: gh was called"

if [ "$failures" -gt 0 ]; then
    exit 1
fi
echo "merge-green: every case matches"
