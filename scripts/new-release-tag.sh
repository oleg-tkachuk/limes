#!/usr/bin/env bash
# new-release-tag.sh — the release tag this run pushed, if any.
#
# Usage: new-release-tag.sh <file listing the tags on HEAD before semantic-release>
#
# Prints the v* tag on HEAD that is not in that list, or nothing. "A v* tag on
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

readonly RELEASE_TAG='^v[0-9]'

before="${1:?usage: new-release-tag.sh <tags-before-file>}"

new() { git tag --points-at HEAD | grep -E "$RELEASE_TAG" | grep -vxF -f "$before" || true; }

# unpublished prints the release tags on HEAD that have no GitHub release.
# grep finding none exits 1, which pipefail would make this function's status.
unpublished() {
    local tag
    { git tag --points-at HEAD | grep -E "$RELEASE_TAG" || true; } | while read -r tag; do
        gh release view "$tag" >/dev/null 2>&1 || echo "$tag"
    done
}

tags="$(new)"
[ -n "$tags" ] || tags="$(unpublished)"
[ -z "$tags" ] || printf '%s\n' "$tags" | head -1
