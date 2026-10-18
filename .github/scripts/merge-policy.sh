# shellcheck shell=bash
# merge-policy.sh — who the automation acts for, and on which branch.
#
# Usage: source gh-retry.sh, then this; REPO names the repository.
#
# Shared by queue-auto-merge.sh, which arms a merge through AUTOMERGE_TOKEN,
# and rebase-behind.sh, which rewrites a branch through it: both act only for
# a pull request into BASE_BRANCH whose author could have pushed the change
# themselves, so the token never does for someone what they could not do.
# auto-merge.yaml's pull_request_target branches filter names the same branch.

# The branch the automation merges into and rebases onto.
# shellcheck disable=SC2034 # read by the scripts that source this one
readonly BASE_BRANCH=main
# The repository permissions that can push a branch, as the collaborator
# permission API names them (maintain reads as write there).
readonly PUSH_PERMISSIONS='^(admin|write)$'

# can_push reports whether login can push to REPO. An author the API does not
# know as a collaborator — an app, a user with no access — cannot, and nor can
# one whose permission cannot be read.
can_push() {
    local permission
    permission="$(gh_retry api "repos/${REPO}/collaborators/$1/permission" --jq .permission 2>/dev/null)" || return 1
    [[ $permission =~ $PUSH_PERMISSIONS ]]
}
