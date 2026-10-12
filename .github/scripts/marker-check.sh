#!/usr/bin/env bash
# marker-check.sh — does a change mark itself breaking on a stable line?
#
# Usage: marker-check.sh <base> <head>
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

merge_base="$(git merge-base "$1" "$2")"
line="$(release_line "$merge_base")"
if ! on_stable_line "$line"; then
    echo "marker-check: no stable release line yet (${line:-no release}); breaking markers cut a minor"
    exit 0
fi
if marks_breaking "$merge_base" "$2"; then
    echo "marker-check: a commit marks a break on the $line line, which would tag a major the Go proxy ignores without a module path move" >&2
    exit 1
fi
echo "marker-check: no breaking marker on the $line line"
