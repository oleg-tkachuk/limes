#!/usr/bin/env bash
# rebase-behind.sh — advance the merge queue: rebase onto main the first pull
# request in line, and only that one, when it has fallen behind.
#
# Usage: GH_TOKEN=… REPO=owner/name rebase-behind.sh
#
# main requires every pull request to be up to date with it before it merges.
# Rebasing every open one after each merge reran CI on all of them, and only
# the first to finish merged, so the rest were rebased and rerun again. The
# queue instead rebases one: the lowest-numbered pull request that would merge
# on its own once green. The rest wait unrebased, and their turn comes when the
# one ahead merges (a push to main) or its CI fails (the run completing), the
# two events auto-merge.yaml runs this on. A first in line that is not behind
# is left alone: its CI is running, or merge-green is about to merge it.
#
# In line: same-repository, non-draft pull requests into main whose author can
# push here, with no known conflict (DIRTY), whose required check has not
# failed at their head, and that something merges once green: GitHub's
# auto-merge is enabled, or merge-green would merge it, as it does every pull
# request that changes no CI definition (merge-policy.sh). Renovate's are left
# out: Renovate rebases and merges its own. A pull request that changes CI
# without auto-merge waits for a person, and would hold the queue if it were in
# it. Whether one is behind is read from the comparison of its head with main,
# not from its merge state: GitHub computes that in the background, and
# seconds after a push to main it still reads CLEAN or UNKNOWN for a branch the
# push left behind.
#
# Only one whose author can push to this repository is rebased, so the token
# never rewrites a branch on behalf of someone who could not have pushed it.
# The rebase names the head it compared (expected_head_sha): a branch pushed
# to since is left as its owner left it. One that cannot be compared or
# rebased — a conflict GitHub finds only while rebasing — is reported, and the
# next in line takes its place.
#
# Without GH_TOKEN it warns and does nothing: a push made with the workflow's
# own GITHUB_TOKEN would start no CI on the rebased branch (auto-merge.yaml).

set -euo pipefail

# shellcheck source=.github/scripts/gh-retry.sh
source "$(dirname "${BASH_SOURCE[0]}")/gh-retry.sh"
# shellcheck source=.github/scripts/merge-policy.sh
source "$(dirname "${BASH_SOURCE[0]}")/merge-policy.sh"

# The most pull requests one run looks at.
readonly PR_LIMIT=200
# The merge state of a pull request whose branch conflicts with main.
readonly DIRTY=DIRTY
# The check main requires, and the conclusions of it that keep a pull
# request in line: any other a finished run of it ends with — failed,
# cancelled, timed out — takes the pull request out until it is pushed to or
# its run is retried, as no merge would follow it. Only the latest run of the
# check counts: a run superseded on the same head — the pull request's title
# edited, say — is cancelled beside the run that replaced it. A run still
# going has no conclusion yet (a status still PENDING), and stays in line.
readonly REQUIRED_CHECK="All checks passed"
readonly PASSING='["SUCCESS", "NEUTRAL", "SKIPPED"]'
readonly PENDING=PENDING

if [ -z "${GH_TOKEN:-}" ]; then
    echo "::warning::AUTOMERGE_TOKEN is not set; the merge queue does not move (see auto-merge.yaml)"
    exit 0
fi
: "${REPO:?REPO names the repository, owner/name}"

# Lowest number first: the order pull requests joined the queue in.
candidates="$(gh_retry pr list --repo "$REPO" --base "$BASE_BRANCH" --state open --limit "$PR_LIMIT" \
    --json number,headRefOid,isDraft,isCrossRepository,mergeStateStatus,author,autoMergeRequest,statusCheckRollup,changedFiles |
    jq -r --arg dirty "$DIRTY" --arg renovate "$RENOVATE" --arg check "$REQUIRED_CHECK" \
        --argjson passing "$PASSING" --arg pending "$PENDING" \
        'sort_by(.number) | .[] | select(.isDraft | not) | select(.isCrossRepository | not)
             | select(.author.login != $renovate)
             | select(.mergeStateStatus != $dirty)
             | select(([.statusCheckRollup[]? | select((.name // .context) == $check)] | sort_by(.startedAt // "") | last
                       | (.conclusion // .state // "")) as $c
                       | $c == "" or $c == $pending or ($passing | index($c)))
             | "\(.number) \(.headRefOid) \(.author.login) \(.autoMergeRequest != null) \(.changedFiles)"')"

# merges_itself reports whether pull request <n>, changing <changed> files,
# is one merge-green merges without GitHub's auto-merge: every file it
# changes is listed, and none is a CI definition.
merges_itself() {
    local entries listed files path
    entries="$(gh_retry api "repos/${REPO}/pulls/$1/files" --paginate --jq '.[] | [.filename, (.previous_filename // "")] | @tsv')" || return 1
    listed="$(grep -c . <<<"$entries" || true)"
    if ! files_listed_in_full "$listed" "$2" "$FILES_API_CAP"; then
        return 1
    fi
    files="$(tr '\t' '\n' <<<"$entries")"
    while IFS= read -r path; do
        [ -n "$path" ] || continue
        if defines_ci "$path"; then
            return 1
        fi
    done <<<"$files"
}

while read -r pr head author auto changed; do
    [ -n "$pr" ] || continue
    if ! can_push "$author"; then
        echo "::notice::#${pr} is by ${author}, who cannot push here; it is not in line"
        continue
    fi
    if [ "$auto" != true ] && ! merges_itself "$pr" "$changed"; then
        echo "::notice::#${pr} changes what CI runs, or more files than were listed, and has no auto-merge; it waits for a person, not in line"
        continue
    fi
    if ! by="$(gh_retry api "repos/${REPO}/compare/${BASE_BRANCH}...${head}" | jq -r '.behind_by')"; then
        echo "::warning::could not compare #${pr} with ${BASE_BRANCH}; the next in line goes first"
        continue
    fi
    if [ "$by" -eq 0 ]; then
        echo "rebase-behind: #${pr} is first in line and up to date"
        exit 0
    fi
    if gh_retry api --method PUT "repos/${REPO}/pulls/${pr}/update-branch" \
        -f update_method=rebase -f expected_head_sha="${head}" >/dev/null; then
        echo "rebase-behind: rebased #${pr}, first in line"
        exit 0
    fi
    echo "::warning::could not rebase #${pr}; it needs a manual update, and the next in line goes first"
done <<<"$candidates"
echo "rebase-behind: no pull request in line"
