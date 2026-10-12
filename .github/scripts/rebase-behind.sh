#!/usr/bin/env bash
# rebase-behind.sh — rebase every open pull request into main that has fallen
# behind it.
#
# Usage: GH_TOKEN=… REPO=owner/name rebase-behind.sh
#
# Same-repository, non-draft pull requests only: a fork's branch belongs to its
# author and a draft is not ready. BEHIND is the only merge state a rebase can
# fix; a conflict (DIRTY) needs a person. One pull request that cannot be
# rebased — a conflict GitHub finds only while rebasing — is reported and does
# not stop the rest.
#
# Without GH_TOKEN it warns and does nothing: a push made with the workflow's
# own GITHUB_TOKEN would start no CI on the rebased branch (auto-merge.yaml).

set -euo pipefail

readonly BASE_BRANCH=main
# The most pull requests one run looks at.
readonly PR_LIMIT=200
readonly BEHIND=BEHIND

if [ -z "${GH_TOKEN:-}" ]; then
    echo "::warning::AUTOMERGE_TOKEN is not set; pull requests not rebased (see auto-merge.yaml)"
    exit 0
fi
: "${REPO:?REPO names the repository, owner/name}"

behind="$(gh pr list --repo "$REPO" --base "$BASE_BRANCH" --state open --limit "$PR_LIMIT" \
    --json number,isDraft,isCrossRepository,mergeStateStatus |
    jq -r --arg behind "$BEHIND" \
        '.[] | select(.isDraft | not) | select(.isCrossRepository | not)
             | select(.mergeStateStatus == $behind) | .number')"

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
