# shellcheck shell=bash
# merge-policy.sh — who the automation acts for, on which branch, and which
# changes it leaves to a person.
#
# Usage: source gh-retry.sh, then this; REPO names the repository.
#
# Shared by merge-green.sh, which merges a pull request through
# AUTOMERGE_TOKEN, and rebase-behind.sh, which rewrites a branch through it:
# both act only for a pull request into BASE_BRANCH whose author could have
# pushed the change themselves, so the token never does for someone what they
# could not do.

# The branch the automation merges into and rebases onto.
# shellcheck disable=SC2034 # read by the scripts that source this one
readonly BASE_BRANCH=main
# The repository permissions that can push a branch, as the collaborator
# permission API names them (maintain reads as write there).
readonly PUSH_PERMISSIONS='^(admin|write)$'
# How gh names Renovate as the author of a pull request. Renovate merges and
# rebases its own, once a release is old enough (renovate.json).
# shellcheck disable=SC2034 # read by the scripts that source this one
readonly RENOVATE=app/renovate

# The paths that decide what CI runs: the workflows and their scripts, the
# tasks CI calls, the linter's configuration, the pinned tools, and the root
# go.mod, whose go and toolchain lines pick the Go every job sets up, a
# workspace at any depth, which overrides every module's requirements in each
# job that runs go from there down, a vendor directory at any depth, which
# replaces the source a module builds, Task's own settings and its cache of
# remote includes and checksums (.task), and Renovate's,
# which decide what Renovate
# merges into main with no person reading it. A pull
# request runs the workflows and tasks of its own head, so one that changes
# them can make "All checks passed" pass without the checks behind it: its
# green check is no evidence, and it is left for a person. Task and
# golangci-lint each read whichever of several file names exists, and Task
# prefers Taskfile.yml to Taskfile.yaml, so a new file under another of those
# names replaces the reviewed one: every spelling counts. A nested module's
# go.mod is left out on purpose: the release raises its requirement of limes
# in a pull request merged this way (require-released-limes.sh).
readonly CI_DEFINITION=(
    '.github/*'
    '[Tt]askfile.*'
    '.taskrc*'
    '.task/*'
    '.golangci.*'
    'tools/*'
    'go.mod'
    'go.work*'
    '*/go.work*'
    'vendor/*'
    '*/vendor/*'
    'renovate.json*'
    '.renovaterc*'
)

# The most files GitHub's pull request files API lists: past it the rest are
# never seen, so a pull request that reaches it is left for a person.
# shellcheck disable=SC2034 # read by the scripts that source this one
readonly FILES_API_CAP=3000
# The most files the compare API lists for two commits, which lists a
# commit's own files rather than those of a head that may move.
# shellcheck disable=SC2034 # read by the scripts that source this one
readonly COMPARE_FILES_CAP=300

# files_listed_in_full reports whether <listed> files of a pull request
# GitHub says changes <changed> are all of them: changed is a count, the two
# agree, and the listing did not stop at <cap>, the cap of the API that
# listed them. A count that is not one — empty, null — is never read as a
# complete listing, and neither is one the listing disagrees with either
# way: more files than changed means the two answers describe different
# heads.
files_listed_in_full() {
    [[ $1 =~ ^[0-9]+$ && $2 =~ ^[0-9]+$ ]] && [ "$1" -eq "$2" ] && [ "$1" -lt "$3" ]
}

# defines_ci reports whether path is one CI_DEFINITION names.
defines_ci() {
    local pattern
    for pattern in "${CI_DEFINITION[@]}"; do
        # shellcheck disable=SC2053 # the glob is the point
        [[ $1 == $pattern ]] && return 0
    done
    return 1
}

# can_push reports whether login can push to REPO. An author the API does not
# know as a collaborator — an app, a user with no access — cannot, and nor can
# one whose permission cannot be read.
can_push() {
    local permission
    permission="$(gh_retry api "repos/${REPO}/collaborators/$1/permission" --jq .permission 2>/dev/null)" || return 1
    [[ $permission =~ $PUSH_PERMISSIONS ]]
}
