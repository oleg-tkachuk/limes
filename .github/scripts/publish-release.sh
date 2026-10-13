#!/usr/bin/env bash
# publish-release.sh — publish the GitHub releases of one module's tags.
#
# Usage: publish-release.sh new <file listing the tags on HEAD before semantic-release> [tag prefix]
#        publish-release.sh left-behind [tag prefix]
#
# `new` publishes the tag this run pushed (new-release-tag.sh), with the notes
# semantic-release wrote to RELEASE_NOTES_FILE, and tells the Go proxy of it.
# `left-behind` publishes every recent tag an earlier run pushed and never
# published (unpublished-tags.sh), as not the latest.
#
# The prefix picks the module: none for the root module, `<dir>/` for the
# module in <dir>. Two things differ between modules sharing one repository:
#
# - GitHub's list of merged pull requests starts at the previous release tag
#   it finds itself, which can be another module's. It is handed the
#   previous tag of the same module instead.
# - A nested module's release is never marked Latest: the repository's Latest
#   is the root module's.

set -euo pipefail

scripts="$(dirname "${BASH_SOURCE[0]}")"
# shellcheck source=.github/scripts/gh-retry.sh
source "$scripts/gh-retry.sh"

readonly USAGE="usage: publish-release.sh new <tags-before-file> [tag-prefix] | left-behind [tag-prefix]"

# previous prints the newest release tag of the module below <tag>, or nothing.
previous() { # tag
    git tag --merged "$1" --list "${prefix}v[0-9]*" --sort=-v:refname | grep -vxF "$1" | head -n 1 || true
}

# create publishes <tag> with the arguments after it, unless it is published
# already, and marks it Latest only when mark_latest says so. A 5xx can
# arrive after the release was made: the retry then meets "already exists",
# and the release standing is success.
create() { # tag, gh release create arguments…
    local tag="$1" start
    shift
    local args=(--repo "$GITHUB_REPOSITORY" --generate-notes --verify-tag)
    start="$(previous "$tag")"
    [ -z "$start" ] || args+=(--notes-start-tag "$start")
    [ "$mark_latest" = true ] || args+=(--latest=false)
    gh_retry release create "$tag" "${args[@]}" "$@" ||
        gh_retry release view "$tag" --repo "$GITHUB_REPOSITORY" >/dev/null
}

mode="${1:?$USAGE}"
case "$mode" in
new)
    before="${2:?$USAGE}"
    prefix="${3:-}"
    # GitHub decides which root release is Latest, as it always has.
    if [ -z "$prefix" ]; then mark_latest=true; else mark_latest=false; fi
    tag="$("$scripts/new-release-tag.sh" "$before" "$prefix")"
    if [ -z "$tag" ]; then
        printf '%s\n' "no new ${prefix}v* tag from this run — no release was due"
        exit 0
    fi
    notes=()
    if [ -s "${RELEASE_NOTES_FILE:-}" ]; then
        notes=(--notes-file "$RELEASE_NOTES_FILE")
    fi
    create "$tag" "${notes[@]}"
    "$scripts/warm-go-proxy.sh" "$tag" ||
        echo "::warning::the Go proxy did not take $tag yet; it will on its own"
    ;;
left-behind)
    prefix="${2:-}"
    # Older than the release it was left behind by: never the Latest.
    mark_latest=false
    "$scripts/unpublished-tags.sh" "$prefix" | while read -r tag; do
        create "$tag"
        printf '%s\n' "published $tag, which an earlier run left without a release"
    done
    ;;
*)
    echo "$USAGE" >&2
    exit 2
    ;;
esac
