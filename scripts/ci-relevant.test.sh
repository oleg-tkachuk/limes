#!/usr/bin/env bash
# ci-relevant.test.sh — ci-relevant.sh skips CI for documentation only, and
# codeql.yaml ignores the same paths.
#
# Runs from `task verify-all` — no Docker, no network.

set -euo pipefail

root="$(git rev-parse --show-toplevel)"
script="$root/scripts/ci-relevant.sh"

failures=0
check() { # name, want, changed paths
    local got
    got="$(printf '%s' "$3" | "$script")"
    if [ "$2" != "$got" ]; then
        printf 'FAIL %s: want %s, got %s\n' "$1" "$2" "$got"
        failures=$((failures + 1))
    fi
}

check "no change" false ""
check "the README" false "README.md"
check "a document" false "docs/diagrams.md"
check "the licence" false "LICENSE"
check "documentation and code" true "$(printf 'README.md\nverifier.go')"
check "Go source" true "verifier.go"
check "a fixture" true "testdata/golden_token.jwt"
check "a workflow" true ".github/workflows/ci.yaml"
check "go.mod" true "go.mod"
check "a file type nobody listed" true "something.new"
[ "$("$script" --all)" = true ] || { echo "FAIL --all"; failures=$((failures + 1)); }

# codeql.yaml's paths-ignore, as globs, against INERT. GitHub's ** is the
# script's *, which already crosses "/".
want="$("$script" --print-inert | sort)"
got="$(yq '.on.push.paths-ignore[]' "$root/.github/workflows/codeql.yaml" | sed 's#\*\*#*#g' | sed 's#^\*/##' | sort)"
if [ "$want" != "$got" ]; then
    printf 'FAIL codeql.yaml paths-ignore differs from INERT:\n%s\n' "$(diff <(echo "$want") <(echo "$got") || true)"
    failures=$((failures + 1))
fi

if [ "$failures" -gt 0 ]; then
    exit 1
fi
echo "ci-relevant: every case matches"
