#!/usr/bin/env bash
# rebase-behind.sh — rebase every open pull request into main that has fallen
# behind it.
#
# Usage: GH_TOKEN=… REPO=owner/name rebase-behind.sh
#
# Same-repository, non-draft pull requests only: a fork's branch belongs to its
# author and a draft is not ready. Renovate's are skipped: Renovate rebases its
# own branches, and stops updating one it finds changed by anyone else. Whether one is behind is read from the
# comparison of its head with main, not from its merge state: GitHub computes
# that in the background, and seconds after a push to main it still reads
# CLEAN or UNKNOWN for a branch the push left behind. A known conflict (DIRTY)
# needs a person and is skipped.
#
# A same-repository head is not always its author's: anyone may open a pull
# request from a branch already here, release/v1 or another person's. Only
# one whose author can push to this repository is rebased, so the token never
# rewrites a branch on behalf of someone who could not have pushed it. Each
# rebase names the head it compared (expected_head_sha): a branch pushed to
# since is left as its owner left it, not rewritten. One pull request that cannot be rebased — a
# conflict GitHub finds only while rebasing — is reported and does not stop the
# rest.
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

if [ -z "${GH_TOKEN:-}" ]; then
    echo "::warning::AUTOMERGE_TOKEN is not set; pull requests not rebased (see auto-merge.yaml)"
    exit 0
fi
: "${REPO:?REPO names the repository, owner/name}"

candidates="$(gh_retry pr list --repo "$REPO" --base "$BASE_BRANCH" --state open --limit "$PR_LIMIT" \
    --json number,headRefOid,isDraft,isCrossRepository,mergeStateStatus,author |
    jq -r --arg dirty "$DIRTY" --arg renovate "$RENOVATE" \
        '.[] | select(.isDraft | not) | select(.isCrossRepository | not)
             | select(.author.login != $renovate)
             | select(.mergeStateStatus != $dirty) | "\(.number) \(.headRefOid) \(.author.login)"')"

behind=""
while read -r pr head author; do
    [ -n "$pr" ] || continue
    if ! can_push "$author"; then
        echo "::notice::#${pr} is by ${author}, who cannot push here; it is not rebased"
        continue
    fi
    if ! by="$(gh_retry api "repos/${REPO}/compare/${BASE_BRANCH}...${head}" | jq -r '.behind_by')"; then
        echo "::warning::could not compare #${pr} with ${BASE_BRANCH}; it is not rebased"
        continue
    fi
    if [ "$by" -gt 0 ]; then
        behind="${behind} ${pr}:${head}"
    fi
done <<<"$candidates"

failed=0
for entry in $behind; do
    pr="${entry%%:*}"
    if gh_retry api --method PUT "repos/${REPO}/pulls/${pr}/update-branch" \
        -f update_method=rebase -f expected_head_sha="${entry#*:}" >/dev/null; then
        echo "rebased #${pr}"
    else
        echo "::warning::could not rebase #${pr}; it needs a manual update"
        failed=$((failed + 1))
    fi
done
echo "rebase-behind: $(wc -w <<<"$behind" | tr -d ' ') behind, ${failed} not rebased"
