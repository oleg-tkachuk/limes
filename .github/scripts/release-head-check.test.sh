#!/usr/bin/env bash
# release-head-check.test.sh — release-head-check.sh releases only the commit
# CI passed.
#
# The case that matters is a head that moved: a pull request merged while the
# dispatching CI run finished. Reporting it as current tags a commit whose own
# checks have not run — unless all it adds is documentation.
#
# Runs from `task verify-all` — no Docker, no network.

set -euo pipefail

script="$(git rev-parse --show-toplevel)/.github/scripts/release-head-check.sh"
work="$(mktemp -d "${TMPDIR:-/tmp}/release-head-check.XXXXXX")"
trap 'rm -rf "$work"' EXIT

cd "$work"
git init -q repo && cd repo
commit() { git -c user.name=t -c user.email=t@example.invalid commit -q --allow-empty -m "$1"; }

failures=0
check() { # name, want, got
    if [ "$2" != "$3" ]; then
        printf 'FAIL %s: want %q, got %q\n' "$1" "$2" "$3"
        failures=$((failures + 1))
    fi
}

commit one
passed=$(git rev-parse HEAD)
check "the head CI passed" "release=true" "$("$script" "$passed")"
check "the head by its short sha" "release=true" "$("$script" "${passed:0:8}")"

# Past it with documentation only: the head's code is the passed commit's,
# and no CI run would dispatch a release for it.
mkdir -p docs
echo note >docs/guide.md
git add docs/guide.md
commit "docs: a note"
check "main moved past it with documentation only" "release=true" "$("$script" "$passed" 2>/dev/null)"

echo 'package m' >m.go
git add m.go
commit "feat: code"
check "main moved past it with code" "release=false" "$("$script" "$passed" 2>/dev/null)"
check "a sha this clone does not have" "release=false" "$("$script" 0000000000000000000000000000000000000000 2>/dev/null)"

# A sha that is not behind the head is never released in its place, even
# when the two differ by documentation only: the side commit forks from the
# head and touches docs alone, so only the ancestry check can refuse it.
git checkout -q -b side
echo other >docs/other.md
git add docs/other.md
commit "docs: elsewhere"
elsewhere=$(git rev-parse HEAD)
git checkout -q -
echo more >>docs/guide.md
git add docs/guide.md
commit "docs: more"
check "a sha off the head's history" "release=false" "$("$script" "$elsewhere" 2>/dev/null)"

if "$script" "" >/dev/null 2>&1; then
    printf 'FAIL %s\n' "an empty sha is accepted"
    failures=$((failures + 1))
fi

if [ "$failures" -gt 0 ]; then
    exit 1
fi
printf '%s\n' "release head check: releases only the commit CI passed"
