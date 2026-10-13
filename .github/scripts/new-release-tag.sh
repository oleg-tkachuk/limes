#!/usr/bin/env bash
# new-release-tag.sh — the release tag this run pushed, if any.
#
# Usage: new-release-tag.sh <file listing the tags on HEAD before semantic-release> [tag prefix]
#
# Prints the release tag on HEAD that is not in that list, or nothing. A
# release tag is the prefix, then vX.Y.Z: no prefix for the root module,
# `<dir>/` for the module in <dir>, so each module's run sees its own tags
# only. "A v* tag on
# HEAD" alone is not the same question: release.yaml is dispatched once per CI
# run on main and always checks out the branch head, so two dispatches can land
# on one commit, and the second would take the first run's tag for its own.
#
# A tag already on HEAD whose GitHub release does not exist is reported too,
# when no tag is new: a run that pushed the tag and failed before publishing
# it. semantic-release does not tag twice, so re-running that run finds no new
# tag; this is how the re-run publishes what the first one left behind. A tag
# with its release is a finished release and stays out.

set -euo pipefail

# shellcheck source=.github/scripts/gh-retry.sh
source "$(dirname "${BASH_SOURCE[0]}")/gh-retry.sh"

before="${1:?usage: new-release-tag.sh <tags-before-file> [tag-prefix]}"
prefix="${2:-}"

# release_tags passes on the lines of stdin that are release tags of the
# module. A glob rather than a regex, so the prefix is matched as it is spelt.
release_tags() {
    local tag
    while read -r tag; do
        [[ "$tag" == "$prefix"v[0-9]* ]] && printf '%s\n' "$tag"
    done
    return 0
}

new() { git tag --points-at HEAD | release_tags | grep -vxF -f "$before" || true; }

# unpublished prints the release tags on HEAD that have no GitHub release.
# A release gh cannot read for a transient reason is retried: read as
# missing, it would be published a second time.
unpublished() {
    local tag
    git tag --points-at HEAD | release_tags | while read -r tag; do
        gh_retry release view "$tag" >/dev/null 2>&1 || echo "$tag"
    done
}

tags="$(new)"
[ -n "$tags" ] || tags="$(unpublished)"
[ -z "$tags" ] || printf '%s\n' "$tags" | head -1
