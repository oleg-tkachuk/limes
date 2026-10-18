#!/usr/bin/env bash
# merge-green.sh — merge the pull requests whose CI run just passed, at the
# head that run tested, unless one of them is a person's to merge.
#
# Usage: GH_TOKEN=… REPO=owner/name HEAD_SHA=<sha> PULL_REQUESTS=<json> merge-green.sh
#
# auto-merge.yaml runs this from main when ci.yaml completes green for a pull
# request: HEAD_SHA is the commit that run tested and PULL_REQUESTS the
# workflow_run's pull_requests array, [{"number": 7, …}]. Each pull request
# is read and decided now, at merge time, and merged with --match-head-commit
# HEAD_SHA: a push since the run moves the head, the merge is refused, and the
# new head's own run decides it. Nothing is armed ahead of time, so there is
# no earlier verdict for a later push to merge under.
#
# Left for a person (merge-policy.sh): a draft — mark a pull request draft to
# hold it —, a fork's, Renovate's, one into another branch than BASE_BRANCH,
# one whose author cannot push here, and one that changes CI_DEFINITION,
# whose green check is no evidence. Its files are read through the pull
# request files API, as GitHub lists them, not from a diff's headers.
#
# Without GH_TOKEN it warns and does nothing: a merge made with the workflow's
# own GITHUB_TOKEN would start no CI on main (auto-merge.yaml).

set -euo pipefail

# shellcheck source=.github/scripts/gh-retry.sh
source "$(dirname "${BASH_SOURCE[0]}")/gh-retry.sh"
# shellcheck source=.github/scripts/merge-policy.sh
source "$(dirname "${BASH_SOURCE[0]}")/merge-policy.sh"

# The state of a pull request still open.
readonly OPEN=OPEN

if [ -z "${GH_TOKEN:-}" ]; then
    echo "::warning::AUTOMERGE_TOKEN is not set; nothing merged (see auto-merge.yaml)"
    exit 0
fi
: "${REPO:?REPO names the repository, owner/name}"
: "${HEAD_SHA:?HEAD_SHA names the commit the CI run tested}"
: "${PULL_REQUESTS:?PULL_REQUESTS is the pull_requests of the CI run, as JSON}"
if ! [[ $HEAD_SHA =~ ^[0-9a-f]{40}$ ]]; then
    echo "::error::HEAD_SHA ${HEAD_SHA} is not a commit id"
    exit 1
fi

# merge_if_green merges pull request $1 at HEAD_SHA if nothing leaves it for
# a person; it reports why not otherwise.
merge_if_green() {
    local pr=$1 view state draft cross author base head path
    view="$(gh_retry pr view "$pr" --repo "$REPO" \
        --json state,isDraft,isCrossRepository,author,baseRefName,headRefOid \
        --jq '[.state, .isDraft, .isCrossRepository, .author.login, .baseRefName, .headRefOid] | @tsv')"
    IFS=$'\t' read -r state draft cross author base head <<<"$view"
    if [ "$state" != "$OPEN" ] || [ "$draft" = true ] || [ "$cross" = true ]; then
        echo "::notice::#$pr is not an open same-repository pull request ready to merge; left alone"
        return 0
    fi
    if [ "$author" = "$RENOVATE" ]; then
        echo "::notice::#$pr is Renovate's, which merges its own; left alone"
        return 0
    fi
    if [ "$base" != "$BASE_BRANCH" ]; then
        echo "::notice::#$pr merges into $base, not $BASE_BRANCH; left for a person to merge"
        return 0
    fi
    if [ "$head" != "$HEAD_SHA" ]; then
        echo "::notice::#$pr has moved past the commit this run tested; its own run decides it"
        return 0
    fi
    if ! can_push "$author"; then
        echo "::notice::#$pr is by $author, who cannot push here; left for a person to merge"
        return 0
    fi
    # A renamed file names its old path only in previous_filename: moving a
    # CI definition out of its place changes what CI runs as surely as
    # deleting it does.
    local files
    files="$(gh_retry api "repos/${REPO}/pulls/${pr}/files" --paginate --jq '.[] | .filename, (.previous_filename // empty)')"
    while IFS= read -r path; do
        [ -n "$path" ] || continue
        if defines_ci "$path"; then
            echo "::notice::#$pr changes $path, which decides what CI runs; left for a person to merge"
            return 0
        fi
    done <<<"$files"
    # Refused if the head moved since it was read: the run tested HEAD_SHA,
    # and the files above are HEAD_SHA's only while the head stays there.
    if gh_retry pr merge "$pr" --repo "$REPO" --merge --match-head-commit "$HEAD_SHA"; then
        echo "::notice::#$pr merged at $HEAD_SHA"
    else
        echo "::warning::#$pr could not be merged at $HEAD_SHA; left as it is"
    fi
}

numbers="$(jq -r '.[].number' <<<"$PULL_REQUESTS")"
while IFS= read -r pr; do
    [ -n "$pr" ] || continue
    if ! [[ $pr =~ ^[0-9]+$ ]]; then
        echo "::error::pull request number ${pr} is not a number"
        exit 1
    fi
    merge_if_green "$pr"
done <<<"$numbers"
