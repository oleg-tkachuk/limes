#!/usr/bin/env bash
# unpublished-tags.sh — release tags below HEAD that have no GitHub release.
#
# Usage: unpublished-tags.sh
#
# Prints, newest first, each of the newest RECENT_RELEASE_TAGS release tags
# reachable from HEAD that has no GitHub release. new-release-tag.sh finds a
# tag left unpublished only while it is still on HEAD; once main has moved
# on, the run that would re-publish it releases nothing of its own, and the
# tag would never get its notes. release.yaml publishes what this prints on
# every run that gets as far as semantic-release.

set -euo pipefail

# shellcheck source=.github/scripts/gh-retry.sh
source "$(dirname "${BASH_SOURCE[0]}")/gh-retry.sh"

readonly RELEASE_TAG_PATTERN='v[0-9]*'
# How far back to look: one failed publish is the case, a few in a row are
# covered, and every tag looked at costs an API call on each release.
readonly RECENT_RELEASE_TAGS=10

git tag --merged HEAD --list "$RELEASE_TAG_PATTERN" --sort=-v:refname |
    head -n "$RECENT_RELEASE_TAGS" |
    while read -r tag; do
        # A release gh cannot read for a transient reason is retried: read
        # as missing, it would be created a second time.
        gh_retry release view "$tag" >/dev/null 2>&1 || echo "$tag"
    done
