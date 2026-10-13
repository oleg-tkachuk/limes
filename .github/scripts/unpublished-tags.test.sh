#!/usr/bin/env bash
# unpublished-tags.test.sh — unpublished-tags.sh names the release tags below
# HEAD with no GitHub release, newest first, however far main has moved past
# them, and no others.
#
# Runs from `task verify-all` — no Docker, no network.

set -euo pipefail

script="$(git rev-parse --show-toplevel)/.github/scripts/unpublished-tags.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# gh stands in for GitHub: `gh release view <tag>` succeeds for the tags
# listed in $work/released, as for a tag whose release exists.
mkdir "$work/bin"
cat >"$work/bin/gh" <<'GH'
#!/usr/bin/env bash
[ "$1 $2" = "release view" ] && grep -qxF "$3" "$RELEASED"
GH
chmod +x "$work/bin/gh"
export PATH="$work/bin:$PATH" RELEASED="$work/released"
: >"$RELEASED"

cd "$work"
git init -q repo && cd repo
commit() { git -c user.name=t -c user.email=t@example.invalid commit -q --allow-empty -m "$1"; }

failures=0
check() { # name, want
    local got
    got="$("$script" | tr '\n' ' ')"
    if [ "$2" != "$got" ]; then
        printf 'FAIL %s: want %q, got %q\n' "$1" "$2" "$got"
        failures=$((failures + 1))
    fi
}

check_prefixed() { # prefix, name, want
    local got
    got="$("$script" "$1" | tr '\n' ' ')"
    if [ "$3" != "$got" ]; then
        printf 'FAIL %s: want %q, got %q\n' "$2" "$3" "$got"
        failures=$((failures + 1))
    fi
}

commit one
check "no tag" ""
git tag v0.1.0
echo v0.1.0 >>"$RELEASED"
check "every tag published" ""

commit two
git tag v0.2.0 # its run failed before publishing
commit three
commit four
check "a tag left behind, main moved on" "v0.2.0 "

git tag vnext
git tag v0.3.0
check "newest first, a tag that is no version left out" "v0.3.0 v0.2.0 "

echo v0.2.0 >>"$RELEASED"
echo v0.3.0 >>"$RELEASED"
git checkout -q -b side HEAD~3
commit side
git tag v9.0.0
git checkout -q -
check "a tag not below HEAD" ""

# Each module's run looks at its own tags only.
git tag sub/v0.1.0
check "the root run beside a nested module's unpublished tag" ""
check_prefixed "sub/" "the nested module's unpublished tag" "sub/v0.1.0 "
echo sub/v0.1.0 >>"$RELEASED"
check_prefixed "sub/" "the nested module's tag published" ""

if [ "$failures" -gt 0 ]; then
    exit 1
fi
echo "unpublished-tags: every case matches"
