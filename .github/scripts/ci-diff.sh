#!/usr/bin/env bash
# ci-diff.sh — which change does this CI run check, and does it reach code?
#
# Usage: BASE=… HEAD=… BEFORE=… AFTER=… ci-diff.sh
#
# A pull request names both ends of its diff (BASE, HEAD); a push names the
# branch's previous head (BEFORE) and its new one (AFTER). Prints
# `relevant=true|false`, a line ci.yaml appends to GITHUB_OUTPUT, by handing
# the changed paths to ci-relevant.sh; the paths and the verdict go to stderr
# for the log.
#
# A push that created the branch reports an all-zero BEFORE, a force-push can
# name a commit this clone does not have, and a run by hand names none at all.
# None of them is a diff, so each means everything.

set -euo pipefail

readonly OUTPUT_KEY=relevant
readonly ZERO_SHA=0000000000000000000000000000000000000000

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [ -n "${BASE:-}" ]; then
    from="$BASE"
    to="${HEAD:-}"
else
    from="${BEFORE:-}"
    to="${AFTER:-}"
fi

if [ -z "$from" ] || [ -z "$to" ] || [ "$from" = "$ZERO_SHA" ] ||
    ! git cat-file -e "${from}^{commit}" 2>/dev/null; then
    echo "no usable base (${from:-none}) — running everything" >&2
    relevant="$("$here/ci-relevant.sh" --all)"
else
    # --no-renames lists a rename's old path too: a CI definition moved
    # away from its place is a change to what CI runs.
    changed="$(git diff --name-only --no-renames "$from" "$to")"
    printf '%s\n' "$changed" >&2
    relevant="$(printf '%s\n' "$changed" | "$here/ci-relevant.sh")"
fi

if [ "$relevant" = false ]; then
    echo "documentation only — the checks below will report as skipped" >&2
fi
echo "${OUTPUT_KEY}=${relevant}"
