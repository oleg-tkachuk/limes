#!/usr/bin/env bash
# tidy-replacing-modules.sh — go mod tidy every nested module that builds the
# root module through a local replace.
#
# Usage: tidy-replacing-modules.sh
#
# Such a module — pgstore, examples — resolves limes from the checkout, so a
# requirement the root raises is raised in its graph too, and its go.mod is
# untidy until it is tidied. deps:update tidies the root alone; CI then fails
# the nested module's tidy and API checks.
#
# The modules are found from their go.mod, not listed here, so a nested
# module that gains or drops the replace needs no edit of this script. A
# module that also replaces another nested module in (examples replaces
# pgstore) is tidied after it: fewer local replaces first.
#
# Only go.mod files git tracks count. Run from anywhere in the repository.
# GO overrides the go command.

set -euo pipefail

if [ "$#" -ne 0 ]; then
    echo "usage: $0" >&2
    exit 2
fi

read -r -a go_cmd <<<"${GO:-go}"
root="$(git rev-parse --show-toplevel)"
cd "$root"

root_module="$("${go_cmd[@]}" mod edit -json go.mod | jq -r '.Module.Path')"

# A local replace is one whose target has no version: a directory.
local_replaces='[.Replace[]? | select(.New.Version == null)]'

ordered=()
while read -r _ dir; do
    ordered+=("$dir")
done < <(
    git ls-files -- '*/go.mod' | while read -r modfile; do
        dir="$(dirname "$modfile")"
        "${go_cmd[@]}" mod edit -json "$modfile" | jq -r --arg root "$root_module" --arg dir "$dir" \
            "$local_replaces"' | select(any(.Old.Path == $root)) | "\(length) \($dir)"'
    done | sort -n -k1,1 -k2,2
)

# The +-form: bash 3.2 reads an empty array under set -u as unbound.
for dir in ${ordered[@]+"${ordered[@]}"}; do
    "${go_cmd[@]}" -C "$dir" mod tidy
    printf 'tidy-replacing-modules: %s tidied\n' "$dir"
done
