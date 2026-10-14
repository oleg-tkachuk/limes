#!/usr/bin/env bash
# require-released-limes.sh — propose that a nested module require the limes
# release this run cut.
#
# Usage: GH_TOKEN=… REPO=owner/name require-released-limes.sh <module dir> <limes tag>
#
# A nested module requires a released limes, and pgstore is released only
# once its tests against the limes it requires pass (release.yaml). So every
# limes release is followed by raising that requirement: this commits it on
# a branch of the module's own and opens its pull request, or updates the
# one still open to the newer release. auto-merge.yaml queues it, and its
# merge releases what the module was waiting for.
#
# The module's go.mod is read and written by `go mod edit`, then tidied. A
# module that does not require limes, or already requires this release or a
# later one, is left alone.
#
# Without GH_TOKEN it warns and does nothing: a push made with the workflow's
# own GITHUB_TOKEN would start no CI on the branch (auto-merge.yaml).
#
# Needs git, go, jq and gh, in a checkout whose origin is this repository.

set -euo pipefail

# shellcheck source=.github/scripts/gh-retry.sh
source "$(dirname "${BASH_SOURCE[0]}")/gh-retry.sh"

# The module every nested module requires.
readonly LIMES_MODULE=github.com/oleg-tkachuk/limes
# The branch a module's requirement is proposed on: <prefix><module dir>.
readonly BRANCH_PREFIX=require-limes/
# The branch the pull request merges into.
readonly BASE_BRANCH=main

module="${1:-}"
tag="${2:-}"
repo="${REPO:?REPO is not set}"
if [ -z "$module" ] || [ ! -f "$module/go.mod" ] || [[ ! "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "!!! usage: require-released-limes.sh <module dir> <limes tag vX.Y.Z> (got '${module}' '${tag}')" >&2
    exit 1
fi
if [ -z "${GH_TOKEN:-}" ]; then
    echo "::warning::AUTOMERGE_TOKEN is not set; $module's requirement of limes $tag not proposed (see release.yaml)"
    exit 0
fi

# Every module starts from main as origin has it, whatever an earlier call
# left checked out or half-written: this runs in a checkout of its own.
branch="$BRANCH_PREFIX$module"
git switch --quiet --discard-changes -C "$branch" "origin/$BASE_BRANCH"
current="$(cd "$module" && go mod edit -json | jq -r --arg m "$LIMES_MODULE" '.Require[]? | select(.Path == $m) | .Version')"
if [ -z "$current" ]; then
    echo "$module does not require $LIMES_MODULE"
    exit 0
fi
if [ "$(printf '%s\n%s\n' "$current" "$tag" | sort -V | tail -n 1)" = "$current" ]; then
    echo "$module already requires limes $current"
    exit 0
fi

title="build($module): require limes $tag"
(cd "$module" && go mod edit -require="$LIMES_MODULE@$tag" && go mod tidy)
git add "$module/go.mod" "$module/go.sum"
git commit --quiet -m "$title"
gh auth setup-git
git push --quiet --force origin "$branch"

body="limes $tag is released. $module required $current; this raises it, so $module is tested against, and released with, the limes it now requires."
pr="$(gh_retry pr list --repo "$repo" --head "$branch" --base "$BASE_BRANCH" --state open --json number --jq '.[0].number // empty')"
if [ -n "$pr" ]; then
    gh_retry pr edit "$pr" --repo "$repo" --title "$title" --body "$body" >/dev/null
    echo "updated #$pr: $title"
else
    gh_retry pr create --repo "$repo" --base "$BASE_BRANCH" --head "$branch" --title "$title" --body "$body"
fi
