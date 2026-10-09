#!/usr/bin/env bash
# release-head-check.sh — is the checked-out head the commit CI passed?
#
# Usage: release-head-check.sh <dispatched-sha>
#
# Prints `release=true` when HEAD is the dispatched commit and `release=false`
# when main has moved past it — a line release.yaml appends to GITHUB_OUTPUT.
# release.yaml checks out the branch head, because semantic-release releases
# only from a branch, and the head can be newer than the commit whose CI
# dispatched the run: a pull request merged while that CI finished. Tagging it
# would release a commit whose own checks had not passed. When the head has
# moved, this run releases nothing: the newer commit's CI dispatches its own
# run once it passes, and semantic-release counts this commit's changes in
# that release.
#
# An empty sha is an error, not a pass. The workflow cannot tell which commit
# CI vouched for, so it does not release.

set -euo pipefail

readonly OUTPUT_KEY=release

sha="${1:-}"
if [ -z "$sha" ]; then
    echo "!!! no dispatched sha: cannot tell which commit CI passed" >&2
    exit 1
fi

head=$(git rev-parse HEAD)
if [ "$(git rev-parse --verify --quiet "${sha}^{commit}" || true)" = "$head" ]; then
    echo "${OUTPUT_KEY}=true"
else
    echo "main moved past $sha to $head; that commit's own CI releases it" >&2
    echo "${OUTPUT_KEY}=false"
fi
