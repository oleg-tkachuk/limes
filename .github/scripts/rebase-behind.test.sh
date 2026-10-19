#!/usr/bin/env bash
# rebase-behind.test.sh — rebase-behind.sh rebases the first pull request in
# line, and only it: lowest number first, among same-repository, non-draft
# ones into main whose author can push here, with no known conflict, whose
# required check has not failed, and that merge themselves (auto-merge on, or
# no CI definition changed). A first in line that is up to date is left alone
# and nothing else is rebased; one that cannot be compared or rebased gives
# its place to the next. Without a token it does nothing.
#
# A fake gh stands in for the API. It logs every call's arguments to CALLS;
# for `pr list` it answers FAKE_PRS cut down to the fields --json asked for, so
# a script reading a field it did not request reads nothing; for a pull
# compare it answers behind_by from FAKE_BEHIND and files from FAKE_FILES
# (none when missing), both keyed by head sha, and fails a sha missing from
# FAKE_BEHIND; for a collaborator's
# permission it answers from FAKE_PERMISSIONS, keyed by login, and fails a
# login missing there; it fails the update-branch call of FAIL_PR.
#
# Runs from `task verify-all` — no Docker, no network.

set -euo pipefail

root="$(git rev-parse --show-toplevel)"
script="$root/.github/scripts/rebase-behind.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

cat >"$work/gh" <<'GH'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$CALLS"
case "$1 $2" in
    "pr list")
        fields=""
        while [ $# -gt 0 ]; do
            if [ "$1" = "--json" ]; then fields="$2"; fi
            shift
        done
        jq --arg f "$fields" '[.[] | with_entries(select(.key as $k | $f | split(",") | index($k)))]' <<<"$FAKE_PRS"
        ;;
    "api repos/"*/permission)
        login="${2%/permission}"
        login="${login##*/}"
        jq -e --arg l "$login" 'has($l)' <<<"$FAKE_PERMISSIONS" >/dev/null || { echo "HTTP 404: Not Found" >&2; exit 1; }
        jq -r --arg l "$login" '.[$l]' <<<"$FAKE_PERMISSIONS"
        ;;
    "api repos/"*/compare/*)
        sha="${2##*...}"
        jq -e --arg sha "$sha" 'has($sha)' <<<"$FAKE_BEHIND" >/dev/null || exit 1
        jq -n --arg sha "$sha" --argjson behind "$FAKE_BEHIND" --argjson files "${FAKE_FILES:-{\}}" \
            '{behind_by: $behind[$sha], files: ($files[$sha] // [])}'
        ;;
    "api --method") [[ "$4" != */"$FAIL_PR"/* ]] ;;
    *) echo "unexpected gh $*" >&2; exit 2 ;;
esac
GH
chmod +x "$work/gh"
export PATH="$work:$PATH" CALLS="$work/calls" REPO=oleg-tkachuk/limes

failures=0
fail() { printf 'FAIL %s\n' "$1"; failures=$((failures + 1)); }

# The update-branch calls made, one per line.
updates() { grep -- '--method PUT' "$CALLS" || true; }
# rebased_only <n> <sha>: the run rebased #n at sha, and nothing else.
rebased_only() {
    [ "$(updates)" = "api --method PUT repos/oleg-tkachuk/limes/pulls/$1/update-branch -f update_method=rebase -f expected_head_sha=$2" ]
}
run() { : >"$CALLS"; out="$(GH_TOKEN=t FAIL_PR="${FAIL_PR:-0}" "$script")" || fail "$1: the run exited non-zero"; }

# pr <number> <sha> [field=json ...]: one pull request as gh lists it, ready
# and in line unless a field says otherwise.
pr() {
    local n="$1" sha="$2"
    shift 2
    local obj
    obj="$(jq -n --argjson n "$n" --arg sha "$sha" '{number: $n, headRefOid: $sha, isDraft: false, isCrossRepository: false,
        mergeStateStatus: "BEHIND", author: {login: "someone"}, autoMergeRequest: null,
        statusCheckRollup: [{name: "All checks passed", conclusion: "SUCCESS"}], changedFiles: 0}')"
    local kv
    for kv in "$@"; do
        obj="$(jq --arg k "${kv%%=*}" --argjson v "${kv#*=}" '.[$k] = $v' <<<"$obj")"
    done
    printf '%s' "$obj"
}
prs() { jq -s . <<<"$*"; }

export FAKE_PERMISSIONS='{"someone": "write", "reader": "read", "keeper": "admin"}'
export FAKE_BEHIND='{"a1": 1, "a2": 1, "a3": 1, "a4": 1, "a5": 1, "a6": 1, "a7": 1, "a8": 1, "a9": 1, "b0": 0, "b1": 1}'
red='[{"name": "All checks passed", "conclusion": "FAILURE"}]'

# Everything ahead of #9 is out of line: a draft, a fork's, a conflict,
# Renovate's, a reader's, a red one, and one changing CI with no auto-merge.
# #9 is rebased; #11, behind it, waits. Listed out of order: the lowest
# number goes first.
export FAKE_FILES='{"a7": [{"filename": ".github/workflows/ci.yaml"}], "a9": [{"filename": "verifier.go"}]}'
export FAKE_PRS
FAKE_PRS="$(prs "$(pr 11 b1)" "$(pr 9 a9 changedFiles=1)" "$(pr 1 a1 isDraft=true)" "$(pr 2 a2 isCrossRepository=true)" \
    "$(pr 3 a3 'mergeStateStatus="DIRTY"')" "$(pr 4 a4 'author={"login":"app/renovate"}')" \
    "$(pr 5 a5 'author={"login":"reader"}')" "$(pr 6 a6 "statusCheckRollup=$red")" "$(pr 7 a7)")"
run "the first in line"
rebased_only 9 a9 || fail "updated: $(updates | tr '\n' ';'), want #9 alone, at the head compared"
[[ "$out" == *"#5 is by reader"* ]] || fail "a skipped author was not reported: $out"
[[ "$out" == *"#7 changes what CI runs"* ]] || fail "a CI change waiting for a person was not reported: $out"
list="$(grep '^pr list' "$CALLS")"
[[ " $list " == *" --base main "* && " $list " == *" --state open "* ]] || fail "pr list asked for the wrong pull requests: $list"

# Only the latest run of the required check counts: a run cancelled when
# another replaced it on the same head reports a failure that no longer
# stands. A failure that is the latest still takes the pull request out.
superseded='[{"name": "All checks passed", "conclusion": "FAILURE", "startedAt": "2026-10-18T18:44:40Z"},
             {"name": "All checks passed", "conclusion": "SUCCESS", "startedAt": "2026-10-18T18:47:44Z"}]'
regressed='[{"name": "All checks passed", "conclusion": "SUCCESS", "startedAt": "2026-10-18T18:44:40Z"},
            {"name": "All checks passed", "conclusion": "FAILURE", "startedAt": "2026-10-18T18:47:44Z"}]'
FAKE_PRS="$(prs "$(pr 1 a1 "statusCheckRollup=$regressed")" "$(pr 2 a2 "statusCheckRollup=$superseded")")"
run "a superseded failed run"
rebased_only 2 a2 || fail "updated: $(updates | tr '\n' ';'), want #2, whose latest run passed, and not #1, whose latest failed"

# Any conclusion a finished latest run ends with but a pass takes the pull
# request out: no merge would follow it. A run still going keeps it in line.
cancelled='[{"name": "All checks passed", "status": "COMPLETED", "conclusion": "CANCELLED"}]'
running='[{"name": "All checks passed", "status": "IN_PROGRESS", "conclusion": ""}]'
FAKE_PRS="$(prs "$(pr 1 a1 "statusCheckRollup=$cancelled")" "$(pr 2 a2 "statusCheckRollup=$running")")"
run "a cancelled latest run"
rebased_only 2 a2 || fail "updated: $(updates | tr '\n' ';'), want #2, whose run is still going, and not #1, whose latest was cancelled"

# A pull request with more files than the compare API listed may hide a CI
# definition: merge-green would leave it for a person, so it is not in line.
FAKE_PRS="$(prs "$(pr 1 a1 changedFiles=2)" "$(pr 2 a2)")"
FAKE_FILES='{"a1": [{"filename": "verifier.go"}]}' run "files left unlisted"
rebased_only 2 a2 || fail "updated: $(updates | tr '\n' ';'), want #2, not #1 with a file unlisted"

# More files listed than counted: the answers are of different heads.
FAKE_PRS="$(prs "$(pr 1 a1 changedFiles=1)" "$(pr 2 a2)")"
FAKE_FILES='{"a1": [{"filename": "a.go"}, {"filename": "b.go"}]}' run "more files listed than changed"
rebased_only 2 a2 || fail "updated: $(updates | tr '\n' ';'), want #2, not #1 whose listing disagrees with its count"

# A count of changed files that is not one leaves the pull request out of line.
FAKE_PRS="$(prs "$(pr 1 a1 changedFiles=null)" "$(pr 2 a2)")"
FAKE_FILES='{"a1": [{"filename": "verifier.go"}]}' run "a count that is not one"
rebased_only 2 a2 || fail "updated: $(updates | tr '\n' ';'), want #2, not #1 with no count of its files"

# A CI change with auto-merge on merges itself, so it is in line.
FAKE_PRS="$(prs "$(pr 7 a7 'autoMergeRequest={"mergeMethod":"MERGE"}')" "$(pr 9 a9)")"
run "a CI change with auto-merge"
rebased_only 7 a7 || fail "updated: $(updates | tr '\n' ';'), want #7, which auto-merge merges"

# A first in line already up to date is waiting on its CI or its merge:
# nothing is rebased, not even one behind it.
FAKE_PRS="$(prs "$(pr 10 b0)" "$(pr 11 b1)")"
run "an up-to-date first"
[ -z "$(updates)" ] || fail "with the first in line up to date, rebased: $(updates | tr '\n' ';')"

# The files are those of the head the rebase names, not of the head the
# pull request has now: a push after the listing moves the head, and the
# rebase at the listed one is refused.
FAKE_PRS="$(prs "$(pr 9 a9 changedFiles=1)")"
run "the files of the listed head"
grep -q -- "^api repos/oleg-tkachuk/limes/compare/main\.\.\.a9$" "$CALLS" || fail "files not listed at the head rebased: $(grep '^api' "$CALLS" | tr '\n' ';')"
! grep -q -- "/files" "$CALLS" || fail "files read from the pull request's moving head: $(grep '^api' "$CALLS" | tr '\n' ';')"

# A first that cannot be rebased, or compared, gives its place to the next.
FAKE_PRS="$(prs "$(pr 1 a1)" "$(pr 2 a2)")"
FAIL_PR=1 run "a failed rebase"
[ "$(updates | wc -l | tr -d ' ')" = 2 ] && [[ "$(updates | tail -n 1)" == */pulls/2/* ]] ||
    fail "after #1 could not be rebased, updated: $(updates | tr '\n' ';'), want #2 next"
[[ "$out" == *"could not rebase #1"* && "$out" == *"rebased #2"* ]] || fail "the log does not say what happened: $out"
FAKE_PRS="$(prs "$(pr 1 c9)" "$(pr 2 a2)")"
run "a failed comparison"
rebased_only 2 a2 || fail "after #1 could not be compared, updated: $(updates | tr '\n' ';'), want #2"
[[ "$out" == *"could not compare #1"* ]] || fail "a failed comparison was not reported: $out"

: >"$CALLS"
out="$(env -u GH_TOKEN "$script")" || fail "a run without a token exited non-zero"
[ ! -s "$CALLS" ] || fail "a run without a token called gh"
[[ "$out" == *"AUTOMERGE_TOKEN is not set"* ]] || fail "a run without a token did not warn: $out"

FAKE_PRS='[]' run "nothing open"
[ -z "$(updates)" ] || fail "a run with nothing open rebased something"

if [ "$failures" -gt 0 ]; then
    exit 1
fi
echo "rebase-behind: every case matches"
