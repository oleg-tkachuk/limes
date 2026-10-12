#!/usr/bin/env bash
# rebase-behind.test.sh — rebase-behind.sh rebases exactly the same-repository,
# non-draft pull requests whose head is behind main, whatever merge state
# GitHub reports for them yet; skips a known conflict; carries on past one it
# cannot compare or rebase; and does nothing without a token.
#
# A fake gh stands in for the API. It logs every call's arguments to CALLS;
# for `pr list` it answers FAKE_PRS cut down to the fields --json asked for, so
# a script reading a field it did not request reads nothing; for a compare it
# answers behind_by from FAKE_BEHIND, keyed by head sha, and fails one sha
# missing there; it fails the update-branch call of FAIL_PR.
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
    "api repos/"*)
        sha="${2##*...}"
        jq -e --arg sha "$sha" 'has($sha)' <<<"$FAKE_BEHIND" >/dev/null || exit 1
        jq --arg sha "$sha" '{behind_by: .[$sha]}' <<<"$FAKE_BEHIND"
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

# #1 fails to rebase; #5, after it, is still rebased. #7 reads CLEAN, as a
# branch a push has just left behind does, and is rebased all the same; #8
# reads BEHIND but its head is current. #9's comparison fails. #10 is
# Renovate's, which Renovate rebases itself.
export FAKE_PRS='[
  {"number": 1, "headRefOid": "a1", "isDraft": false, "isCrossRepository": false, "mergeStateStatus": "BEHIND", "author": {"login": "someone"}},
  {"number": 2, "headRefOid": "a2", "isDraft": true,  "isCrossRepository": false, "mergeStateStatus": "BEHIND", "author": {"login": "someone"}},
  {"number": 3, "headRefOid": "a3", "isDraft": false, "isCrossRepository": true,  "mergeStateStatus": "BEHIND", "author": {"login": "someone"}},
  {"number": 4, "headRefOid": "a4", "isDraft": false, "isCrossRepository": false, "mergeStateStatus": "DIRTY", "author": {"login": "someone"}},
  {"number": 5, "headRefOid": "a5", "isDraft": false, "isCrossRepository": false, "mergeStateStatus": "BEHIND", "author": {"login": "someone"}},
  {"number": 6, "headRefOid": "a6", "isDraft": false, "isCrossRepository": false, "mergeStateStatus": "CLEAN", "author": {"login": "someone"}},
  {"number": 7, "headRefOid": "a7", "isDraft": false, "isCrossRepository": false, "mergeStateStatus": "CLEAN", "author": {"login": "someone"}},
  {"number": 8, "headRefOid": "a8", "isDraft": false, "isCrossRepository": false, "mergeStateStatus": "BEHIND", "author": {"login": "someone"}},
  {"number": 9, "headRefOid": "a9", "isDraft": false, "isCrossRepository": false, "mergeStateStatus": "UNKNOWN", "author": {"login": "someone"}},
  {"number": 10, "headRefOid": "b0", "isDraft": false, "isCrossRepository": false, "mergeStateStatus": "BEHIND", "author": {"login": "app/renovate"}}
]'
export FAKE_BEHIND='{"a1": 1, "a2": 1, "a3": 1, "a4": 1, "a5": 2, "a6": 0, "a7": 1, "a8": 0, "b0": 1}'

: >"$CALLS"
out="$(GH_TOKEN=t FAIL_PR=1 "$script")" || fail "a run with one failed rebase exited non-zero"
want="api --method PUT repos/oleg-tkachuk/limes/pulls/1/update-branch -f update_method=rebase
api --method PUT repos/oleg-tkachuk/limes/pulls/5/update-branch -f update_method=rebase
api --method PUT repos/oleg-tkachuk/limes/pulls/7/update-branch -f update_method=rebase"
[ "$(updates)" = "$want" ] || fail "updated: $(updates | tr '\n' ';'), want #1, #5 and #7 rebased"
list="$(grep '^pr list' "$CALLS")"
[[ " $list " == *" --base main "* && " $list " == *" --state open "* ]] ||
    fail "pr list asked for the wrong pull requests: $list"
grep -q '^api repos/oleg-tkachuk/limes/compare/main\.\.\.a5$' "$CALLS" || fail "#5 was not compared with main"
! grep -Eq 'compare/main\.\.\.(a[234]|b0)$' "$CALLS" || fail "a draft, a fork's, a conflicting or Renovate's pull request was compared"
[[ "$out" == *"could not rebase #1"* && "$out" == *"rebased #5"* ]] || fail "the log does not say what happened: $out"
[[ "$out" == *"could not compare #9"* ]] || fail "a failed comparison was not reported: $out"

: >"$CALLS"
out="$(env -u GH_TOKEN "$script")" || fail "a run without a token exited non-zero"
[ ! -s "$CALLS" ] || fail "a run without a token called gh"
[[ "$out" == *"AUTOMERGE_TOKEN is not set"* ]] || fail "a run without a token did not warn: $out"

: >"$CALLS"
FAKE_PRS='[]' GH_TOKEN=t FAIL_PR=0 "$script" >/dev/null || fail "a run with nothing open exited non-zero"
[ -z "$(updates)" ] || fail "a run with nothing open rebased something"

if [ "$failures" -gt 0 ]; then
    exit 1
fi
echo "rebase-behind: every case matches"
