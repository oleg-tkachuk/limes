#!/usr/bin/env bash
# marker-check.sh — does a change mark itself breaking on a stable line?
#
# Usage: marker-check.sh <base> <head> [module]
#   PR_TITLE, when set, is the pull request's title, which becomes the body
#   of its merge commit.
#
# From v1.0.0 on — once a v1 or later tag is reachable from the merge base of
# <base> and <head> — no commit may mark itself breaking: semantic-release
# would cut a major, which the Go proxy ignores without a /vN module path,
# and every release after it would be refused (major-guard.cjs). A v2 is a
# module path move, planned on its own.
#
# Before v1.0.0 a marker cuts a minor, and passes when the change also edits
# the migration guide: a break no entry describes leaves every consumer to
# rebuild it from the diff.
#
# Each module is checked on its own line, from the commits that touch it
# (breaking-markers.sh); the root module when none is named. One migration
# guide covers every module.
#
# Separate from api-break-check.sh, which needs Go, so it runs on every pull
# request — one that touches only documentation included: a marker there
# breaks the release as surely as one beside code.

set -euo pipefail

if [ "$#" -lt 2 ] || [ "$#" -gt 3 ]; then
    echo "usage: $0 <base> <head> [module]" >&2
    exit 2
fi

# shellcheck source=.github/scripts/breaking-markers.sh
source "$(dirname "${BASH_SOURCE[0]}")/breaking-markers.sh"
select_module "${3:-$ROOT_MODULE}"

# A title that reads as a footer in the merge commit's body marks a break no
# commit of the change carries: refused on any line, as the commits' markers
# are what a break is declared with.
if [ -n "${PR_TITLE:-}" ] && title_marks_breaking "$PR_TITLE"; then
    echo "marker-check: $MODULE_NAME: the title reads as a BREAKING CHANGE footer in the merge commit; mark the commit instead" >&2
    exit 1
fi

# Where each break before v1.0.0 is described as what it was and what it is.
readonly MIGRATION_GUIDE=docs/migration.md

merge_base="$(git merge-base "$1" "$2")"
line="$(release_line "$merge_base")"
if ! stable_at "$merge_base"; then
    if marks_breaking "$merge_base" "$2" && git diff --quiet "$merge_base" "$2" -- "$MIGRATION_GUIDE"; then
        echo "marker-check: $MODULE_NAME: a commit marks a break, and $MIGRATION_GUIDE has no entry for it" >&2
        exit 1
    fi
    echo "marker-check: $MODULE_NAME: no stable release line yet (${line:-no release}); breaking markers cut a minor"
    exit 0
fi
if marks_breaking "$merge_base" "$2"; then
    echo "marker-check: $MODULE_NAME: a commit marks a break on a stable line (${line:-untagged yet}), which would tag a major the Go proxy ignores without a module path move" >&2
    exit 1
fi
echo "marker-check: $MODULE_NAME: no breaking marker on the stable line (${line:-untagged yet})"
