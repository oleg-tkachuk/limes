#!/usr/bin/env bash
# ci-relevant.sh — does a change reach anything CI checks?
#
# Usage: git diff --name-only <from> <to> | ci-relevant.sh
#        ci-relevant.sh --all
#
# Prints `true` when any changed path is outside INERT, `false` when every one
# is documentation. An EXCLUSION list, and the direction matters: a file type
# nobody has thought about yet is relevant by default. --all prints `true`, for
# a run with no diff to classify. codeql.yaml's paths-ignore mirrors INERT;
# ci-relevant.test.sh fails when the two disagree.

set -euo pipefail

INERT=(
    '*.md'
    'docs/*'
    'LICENSE'
    'NOTICE'
    '.gitignore'
    '*.png'
    '*.jpg'
    '*.jpeg'
    '*.svg'
    '*.gif'
)

if [ "${1:-}" = "--print-inert" ]; then
    printf '%s\n' "${INERT[@]}"
    exit 0
fi
if [ "${1:-}" = "--all" ]; then
    echo true
    exit 0
fi

inert() {
    local pattern
    for pattern in "${INERT[@]}"; do
        # shellcheck disable=SC2053 # the glob is the point
        [[ $1 == $pattern ]] && return 0
    done
    return 1
}

while IFS= read -r path || [ -n "$path" ]; do
    [ -n "$path" ] || continue
    if ! inert "$path"; then
        echo true
        exit 0
    fi
done
echo false
