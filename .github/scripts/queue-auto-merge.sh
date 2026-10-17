#!/usr/bin/env bash
# queue-auto-merge.sh — queue a pull request to merge once it is green, unless
# it changes what CI checks.
#
# Usage: GH_TOKEN=… REPO=owner/name PR=<number> ACTION=<event action> queue-auto-merge.sh
#
# A pull request runs the workflows and tasks in its own head, so one that
# changes them can make "All checks passed" pass without the checks behind it:
# its green check is no evidence. Such a pull request is left for a person to
# read and merge. CI_DEFINITION is what decides what CI runs; a path under it
# leaves the pull request alone. auto-merge.yaml runs this from main, never
# from the pull request, so a pull request cannot rewrite the rule that holds
# it.
#
# A pull request is queued only when it opens or becomes ready (ACTION other
# than synchronize). A push to it (ACTION=synchronize) never queues it again —
# a maintainer may have taken it off the queue to hold it — but one that now
# changes CI_DEFINITION takes it off the queue: the gate read its files once,
# and a later push would otherwise merge unread under the earlier verdict.
#
# Without GH_TOKEN it warns and does nothing: a merge made with the workflow's
# own GITHUB_TOKEN would start no CI on main (auto-merge.yaml).

set -euo pipefail

# shellcheck source=.github/scripts/gh-retry.sh
source "$(dirname "${BASH_SOURCE[0]}")/gh-retry.sh"

# The paths that decide what CI runs: the workflows and their scripts, the
# tasks CI calls, the linter's configuration, the pinned tools, and the root
# go.mod, whose go and toolchain lines pick the Go every job sets up. Task
# and golangci-lint each read whichever of several file names exists, and
# Task prefers Taskfile.yml to Taskfile.yaml, so a new file under another of
# those names replaces the reviewed one: every spelling counts. A nested
# module's go.mod is left out on purpose: the release raises its requirement
# of limes in a pull request this queues (require-released-limes.sh).
CI_DEFINITION=(
    '.github/*'
    '[Tt]askfile.*'
    '.golangci.*'
    'tools/*'
    'go.mod'
)

# The pull_request_target action of a push to the pull request's branch.
readonly SYNCHRONIZE=synchronize

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
: "${ACTION:?ACTION names the pull_request_target action}"

# dequeue takes the pull request off the merge queue when it is on it.
dequeue() {
    local queued
    queued="$(gh_retry pr view "$PR" --repo "$REPO" --json autoMergeRequest --jq '.autoMergeRequest != null')"
    if [ "$queued" = true ]; then
        gh_retry pr merge "$PR" --repo "$REPO" --disable-auto
        echo "::notice::#$PR taken off the merge queue"
    fi
}

# A diff that cannot be read fails the step, so the pull request is left
# alone rather than queued unread. On a push it may be one that changes CI,
# and the queue holds an earlier verdict, so it comes off the queue first.
if ! changed="$(gh_retry pr diff "$PR" --repo "$REPO" --name-only)"; then
    echo "::error::#$PR: its diff could not be read; not queued"
    if [ "$ACTION" = "$SYNCHRONIZE" ]; then
        dequeue
    fi
    exit 1
fi
while IFS= read -r path; do
    [ -n "$path" ] || continue
    if defines_ci "$path"; then
        echo "::notice::#$PR changes $path, which decides what CI runs; left for a person to merge"
        dequeue
        exit 0
    fi
done <<<"$changed"

if [ "$ACTION" = "$SYNCHRONIZE" ]; then
    exit 0
fi
gh_retry pr merge "$PR" --repo "$REPO" --auto --merge
