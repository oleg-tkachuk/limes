#!/usr/bin/env bash
# api-break-check.sh — does a change break the Go API without saying so?
#
# Usage: api-break-check.sh <base> <head> [module]
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
# A command — a main package — exports nothing a consumer can import, so
# removing one, or changing what it declares, breaks no API and is left out.
#
# From v1.0.0 on — once a v1 or later tag is reachable from the merge base —
# no break passes, marked or not: it needs a /vN module path, planned on its
# own. marker-check.sh keeps the markers off that line.
#
# The module is the one named, as .github/release/modules.cjs scopes it — the
# root module when none is — and only the commits that touch it count as its
# markers (breaking-markers.sh). A module not yet at the merge base has no
# API to break. Run from anywhere in the repository. APIDIFF overrides the
# apidiff command; by default it is the version tools/go.mod pins.

set -euo pipefail

if [ "$#" -lt 2 ] || [ "$#" -gt 3 ]; then
    echo "usage: $0 <base> <head> [module]" >&2
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

# shellcheck source=.github/scripts/breaking-markers.sh
source "$(dirname "${BASH_SOURCE[0]}")/breaking-markers.sh"
select_module "${3:-$ROOT_MODULE}"
# The module's directory, from the repository root: `.` for the root module.
dir="${MODULE_DIR:-.}"

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
git worktree add --quiet --detach "$work/head" "$head"
for tree in base head; do
    if [ ! -f "$work/$tree/$dir/go.mod" ]; then
        echo "api-break-check: $MODULE_NAME: no module at the $tree, so no API to compare"
        exit 0
    fi
done
# The path the change leaves the module at, which its API is read under.
module="$(cd "$work/head/$dir" && go mod edit -json | jq -r .Module.Path)"
(cd "$work/base/$dir" && "${apidiff[@]}" -m -w "$work/base.api" "$module")
breaks="$(cd "$work/head/$dir" && "${apidiff[@]}" -m -incompatible "$work/base.api" "$module")"

# commands prints the import path of each main package of the module in the
# tree at $1.
commands() {
    (cd "$1/$dir" && go list -f '{{if eq .Name "main"}}{{.ImportPath}}{{end}}' ./...)
}

# apidiff names a removed package by its import path, and a member of a
# package below the module's root by the package's path from that root:
# one line of each per command, matched up to the separator after the name.
: >"$work/commands"
while IFS= read -r command; do
    [ -n "$command" ] || continue
    printf -- '- package %s: \n- ./%s.\n' "$command" "${command#"$module"/}" >>"$work/commands"
done < <({ commands "$work/base"; commands "$work/head"; } | sort -u)
breaks="$(grep -v -F -f "$work/commands" <<<"$breaks" || true)"

if [ -z "$breaks" ]; then
    echo "api-break-check: $MODULE_NAME: no incompatible API change"
    exit 0
fi

printf 'Incompatible API changes since %s:\n%s\n' "$merge_base" "$breaks"

line="$(release_line "$merge_base")"
if stable_at "$merge_base"; then
    echo "api-break-check: $MODULE_NAME: the API is stable on this line (${line:-untagged yet}); an incompatible change needs a new module path" >&2
    exit 1
fi
if marks_breaking "$merge_base" "$head"; then
    echo "api-break-check: $MODULE_NAME: a commit marks the change breaking"
    exit 0
fi
echo "api-break-check: $MODULE_NAME: no commit marks these changes breaking — add ! to the subject or a BREAKING CHANGE: footer" >&2
exit 1
