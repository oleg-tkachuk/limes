#!/usr/bin/env bash
# api-break-check.sh — does a change break the Go API without saying so?
#
# Usage: api-break-check.sh <base> <head>
#
# Compares the exported API of the module at the merge base of <base> and
# <head> with the one at <head>, using apidiff. An incompatible change passes
# only when a commit in base..head marks itself breaking — `type!:` or
# `type(scope)!:` in its subject, or a `BREAKING CHANGE:` footer — since that
# marker is what makes semantic-release cut the version a consumer needs to
# notice. Without it the break ships as a patch or a feature.
#
# A commit marked breaking with no API change passes: the wire format and
# behaviour can break without the API doing so.
#
# Run from the module's root. APIDIFF overrides the apidiff command; by
# default it is the version tools/go.mod pins.

set -euo pipefail

if [ "$#" -ne 2 ]; then
    echo "usage: $0 <base> <head>" >&2
    exit 2
fi
base="$1"
head="$2"

root="$(git rev-parse --show-toplevel)"
# The default as an array, so a checkout path with a space stays one word;
# APIDIFF, a command line of its own, is split on whitespace.
apidiff=(go tool "-modfile=$root/tools/go.mod" apidiff)
if [ -n "${APIDIFF:-}" ]; then
    read -r -a apidiff <<<"$APIDIFF"
fi
module="$(go mod edit -json | jq -r .Module.Path)"

# The subject marker and the footer Conventional Commits 1.0 define, as the
# release's commit parser reads them: either case, any word as the type, and
# the footer after leading spaces, a | or a *, followed by a colon or a space,
# on any line but the subject. The type is ASCII, as the parser's \w is; a
# scope runs to the last ) before the !, as its (.*) does.
# Anything it reads as breaking, this must too.
SUBJECT_MARK='^[A-Za-z0-9_]*(\(.*\))?!: '
FOOTER_MARK='^[[:space:]|*]*BREAKING[ -]CHANGE[:[:space:]]'

work="$(mktemp -d)"
cleanup() {
    local tree
    for tree in base head; do
        git worktree remove --force "$work/$tree" >/dev/null 2>&1 || true
    done
    rm -rf "$work"
}
trap cleanup EXIT

merge_base="$(git merge-base "$base" "$head")"
git worktree add --quiet --detach "$work/base" "$merge_base"
(cd "$work/base" && "${apidiff[@]}" -m -w "$work/base.api" "$module")
git worktree add --quiet --detach "$work/head" "$head"
breaks="$(cd "$work/head" && "${apidiff[@]}" -m -incompatible "$work/base.api" "$module")"

if [ -z "$breaks" ]; then
    echo "api-break-check: no incompatible API change"
    exit 0
fi

printf 'Incompatible API changes since %s:\n%s\n' "$merge_base" "$breaks"

marked=false
while IFS= read -r -d '' message; do
    if grep -qiE "$SUBJECT_MARK" <<<"$(head -n 1 <<<"$message")" ||
        grep -qiE "$FOOTER_MARK" <<<"$(tail -n +2 <<<"$message")"; then
        marked=true
        break
    fi
done < <(git log -z --format=%B "$merge_base..$head")

if [ "$marked" = true ]; then
    echo "api-break-check: a commit marks the change breaking"
    exit 0
fi
echo "api-break-check: no commit marks these changes breaking — add ! to the subject or a BREAKING CHANGE: footer" >&2
exit 1
