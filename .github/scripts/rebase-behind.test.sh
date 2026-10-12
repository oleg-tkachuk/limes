#!/usr/bin/env bash
# rebase-behind.test.sh — rebase-behind.sh rebases exactly the same-repository,
# non-draft pull requests that are BEHIND, carries on past one it cannot
# rebase, and does nothing without a token.
#
# A fake gh stands in for the API: it lists FAKE_PRS for `pr list`, logs every
# update-branch call to CALLS, and fails the one for FAIL_PR.
#
# Runs from `task verify-all` — no Docker, no network.

set -euo pipefail

root="$(git rev-parse --show-toplevel)"
script="$root/.github/scripts/rebase-behind.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

cat >"$work/gh" <<'GH'
#!/usr/bin/env bash
case "$1 $2" in
    "pr list") printf '%s\n' "$FAKE_PRS" ;;
    "api --method")
        printf '%s\n' "$4" >>"$CALLS"
        [[ "$4" != */"$FAIL_PR"/* ]]
        ;;
    *) echo "unexpected gh $*" >&2; exit 2 ;;
esac
GH
chmod +x "$work/gh"
export PATH="$work:$PATH" CALLS="$work/calls" REPO=oleg-tkachuk/limes

failures=0
fail() { printf 'FAIL %s\n' "$1"; failures=$((failures + 1)); }

export FAKE_PRS='[
  {"number": 1, "isDraft": false, "isCrossRepository": false, "mergeStateStatus": "BEHIND"},
  {"number": 2, "isDraft": true,  "isCrossRepository": false, "mergeStateStatus": "BEHIND"},
  {"number": 3, "isDraft": false, "isCrossRepository": true,  "mergeStateStatus": "BEHIND"},
  {"number": 4, "isDraft": false, "isCrossRepository": false, "mergeStateStatus": "DIRTY"},
  {"number": 5, "isDraft": false, "isCrossRepository": false, "mergeStateStatus": "BEHIND"},
  {"number": 6, "isDraft": false, "isCrossRepository": false, "mergeStateStatus": "CLEAN"}
]'

# #1 fails to rebase; #5, after it, is still rebased.
: >"$CALLS"
out="$(GH_TOKEN=t FAIL_PR=1 "$script")" || fail "a run with one failed rebase exited non-zero"
want="repos/oleg-tkachuk/limes/pulls/1/update-branch
repos/oleg-tkachuk/limes/pulls/5/update-branch"
[ "$(cat "$CALLS")" = "$want" ] || fail "rebased $(tr '\n' ' ' <"$CALLS"), want #1 and #5 only"
[[ "$out" == *"could not rebase #1"* && "$out" == *"rebased #5"* ]] || fail "the log does not say what happened: $out"

: >"$CALLS"
out="$(env -u GH_TOKEN "$script")" || fail "a run without a token exited non-zero"
[ ! -s "$CALLS" ] || fail "a run without a token rebased something"
[[ "$out" == *"AUTOMERGE_TOKEN is not set"* ]] || fail "a run without a token did not warn: $out"

: >"$CALLS"
FAKE_PRS='[]' GH_TOKEN=t FAIL_PR=0 "$script" >/dev/null || fail "a run with nothing behind exited non-zero"
[ ! -s "$CALLS" ] || fail "a run with nothing behind rebased something"

if [ "$failures" -gt 0 ]; then
    exit 1
fi
echo "rebase-behind: every case matches"
