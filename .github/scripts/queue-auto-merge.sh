#!/usr/bin/env bash
# queue-auto-merge.sh — queue a pull request to merge once it is green, unless
# it changes what CI checks.
#
# Usage: GH_TOKEN=… REPO=owner/name PR=<number> queue-auto-merge.sh
#
# A pull request runs the workflows and tasks in its own head, so one that
# changes them can make "All checks passed" pass without the checks behind it:
# its green check is no evidence. Such a pull request is left for a person to
# read and merge. CI_DEFINITION is what decides what CI runs; a path under it
# leaves the pull request alone. auto-merge.yaml runs this from main, never
# from the pull request, so a pull request cannot rewrite the rule that holds
# it.
#
# Without GH_TOKEN it warns and does nothing: a merge made with the workflow's
# own GITHUB_TOKEN would start no CI on main (auto-merge.yaml).

set -euo pipefail

# shellcheck source=.github/scripts/gh-retry.sh
source "$(dirname "${BASH_SOURCE[0]}")/gh-retry.sh"

# The paths that decide what CI runs: the workflows and their scripts, the
# tasks CI calls, the linter's configuration and the pinned tools.
CI_DEFINITION=(
    '.github/*'
    'Taskfile.yaml'
    '.golangci.yml'
    'tools/*'
)

# defines_ci reports whether path is one CI_DEFINITION names.
defines_ci() {
    local pattern
    for pattern in "${CI_DEFINITION[@]}"; do
        # shellcheck disable=SC2053 # the glob is the point
        [[ $1 == $pattern ]] && return 0
    done
    return 1
}

if [ -z "${GH_TOKEN:-}" ]; then
    echo "::warning::AUTOMERGE_TOKEN is not set; auto-merge left off (see auto-merge.yaml)"
    exit 0
fi
: "${REPO:?REPO names the repository, owner/name}"
: "${PR:?PR names the pull request, by number}"

# A diff that cannot be read fails the step, so the pull request is left
# alone rather than queued unread.
changed="$(gh_retry pr diff "$PR" --repo "$REPO" --name-only)"
while IFS= read -r path; do
    [ -n "$path" ] || continue
    if defines_ci "$path"; then
        echo "::notice::#$PR changes $path, which decides what CI runs; left for a person to merge"
        exit 0
    fi
done <<<"$changed"

gh_retry pr merge "$PR" --repo "$REPO" --auto --merge
