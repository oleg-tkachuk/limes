#!/usr/bin/env bash
# marker-check.sh — does a change mark itself breaking on a stable line?
#
# Usage: marker-check.sh <base> <head>
#   PR_TITLE, when set, is the pull request's title, which becomes the body
#   of its merge commit.
#
# From v1.0.0 on — once a v1 or later tag is reachable from the merge base of
# <base> and <head> — no commit may mark itself breaking: semantic-release
# would cut a major, which the Go proxy ignores without a /vN module path,
# and every release after it would be refused (major-guard.cjs). A v2 is a
# module path move, planned on its own.
#
# Separate from api-break-check.sh, which needs Go, so it runs on every pull
# request — one that touches only documentation included: a marker there
# breaks the release as surely as one beside code. Before v1.0.0 it passes.

set -euo pipefail

if [ "$#" -ne 2 ]; then
    echo "usage: $0 <base> <head>" >&2
    exit 2
fi

# shellcheck source=.github/scripts/breaking-markers.sh
source "$(dirname "${BASH_SOURCE[0]}")/breaking-markers.sh"

# A title that reads as a footer in the merge commit's body marks a break no
# commit of the change carries: refused on any line, as the commits' markers
# are what a break is declared with.
if [ -n "${PR_TITLE:-}" ] && title_marks_breaking "$PR_TITLE"; then
    echo "marker-check: the title reads as a BREAKING CHANGE footer in the merge commit; mark the commit instead" >&2
    exit 1
fi

merge_base="$(git merge-base "$1" "$2")"
line="$(release_line "$merge_base")"
if ! stable_at "$merge_base"; then
    echo "marker-check: no stable release line yet (${line:-no release}); breaking markers cut a minor"
    exit 0
fi
if marks_breaking "$merge_base" "$2"; then
    echo "marker-check: a commit marks a break on a stable line (${line:-untagged yet}), which would tag a major the Go proxy ignores without a module path move" >&2
    exit 1
fi
echo "marker-check: no breaking marker on the stable line (${line:-untagged yet})"
