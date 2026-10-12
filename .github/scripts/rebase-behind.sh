#!/usr/bin/env bash
# rebase-behind.sh — rebase every open pull request into main that has fallen
# behind it.
#
# Usage: GH_TOKEN=… REPO=owner/name rebase-behind.sh
#
# Same-repository, non-draft pull requests only: a fork's branch belongs to its
# author and a draft is not ready. Whether one is behind is read from the
# comparison of its head with main, not from its merge state: GitHub computes
# that in the background, and seconds after a push to main it still reads
# CLEAN or UNKNOWN for a branch the push left behind. A known conflict (DIRTY)
# needs a person and is skipped. One pull request that cannot be rebased — a
# conflict GitHub finds only while rebasing — is reported and does not stop the
# rest.
#
# Without GH_TOKEN it warns and does nothing: a push made with the workflow's
# own GITHUB_TOKEN would start no CI on the rebased branch (auto-merge.yaml).

set -euo pipefail

readonly BASE_BRANCH=main
# The most pull requests one run looks at.
readonly PR_LIMIT=200
# The merge state of a pull request whose branch conflicts with main.
readonly DIRTY=DIRTY

if [ -z "${GH_TOKEN:-}" ]; then
    echo "::warning::AUTOMERGE_TOKEN is not set; pull requests not rebased (see auto-merge.yaml)"
    exit 0
fi
: "${REPO:?REPO names the repository, owner/name}"

candidates="$(gh pr list --repo "$REPO" --base "$BASE_BRANCH" --state open --limit "$PR_LIMIT" \
    --json number,headRefOid,isDraft,isCrossRepository,mergeStateStatus |
    jq -r --arg dirty "$DIRTY" \
        '.[] | select(.isDraft | not) | select(.isCrossRepository | not)
             | select(.mergeStateStatus != $dirty) | "\(.number) \(.headRefOid)"')"

behind=""
while read -r pr head; do
    [ -n "$pr" ] || continue
    if ! by="$(gh api "repos/${REPO}/compare/${BASE_BRANCH}...${head}" | jq -r '.behind_by')"; then
        echo "::warning::could not compare #${pr} with ${BASE_BRANCH}; it is not rebased"
        continue
    fi
    if [ "$by" -gt 0 ]; then
        behind="${behind} ${pr}"
    fi
done <<<"$candidates"

failed=0
for pr in $behind; do
    if gh api --method PUT "repos/${REPO}/pulls/${pr}/update-branch" -f update_method=rebase >/dev/null; then
        echo "rebased #${pr}"
    else
        echo "::warning::could not rebase #${pr}; it needs a manual update"
        failed=$((failed + 1))
    fi
done
echo "rebase-behind: $(wc -w <<<"$behind" | tr -d ' ') behind, ${failed} not rebased"
