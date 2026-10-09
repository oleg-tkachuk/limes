#!/usr/bin/env bash
# new-release-tag.test.sh — new-release-tag.sh names only the tag a run added.
#
# The case that matters is the second dispatch: HEAD already carries the
# release tag from an earlier run, and nothing new was tagged. Reporting that
# tag as new sends release.yaml into `gh release create` for a release that
# exists, which fails the run.
#
# Runs from `task verify-all` — no Docker, no network.

set -euo pipefail

script="$(git rev-parse --show-toplevel)/scripts/new-release-tag.sh"
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
git -c user.name=t -c user.email=t@example.invalid commit -q --allow-empty -m one
before="$work/before"

failures=0
check() { # name, want, got
    if [ "$2" != "$3" ]; then
        printf 'FAIL %s: want %q, got %q\n' "$1" "$2" "$3"
        failures=$((failures + 1))
    fi
}

# succeeds: the script exits 0. release.yaml runs it under set -e, so a
# nonzero exit fails the release even when the output is right.
succeeds() { # name
    if ! "$script" "$before" >/dev/null; then
        printf 'FAIL %s: exit status is not 0\n' "$1"
        failures=$((failures + 1))
    fi
}

: >"$before"
check "no tag on HEAD" "" "$("$script" "$before")"
succeeds "no tag on HEAD"

git tag baseline
check "a tag that is no version" "" "$("$script" "$before")"
succeeds "a tag that is no version"

git tag v0.2.0
check "the tag this run pushed" "v0.2.0" "$("$script" "$before")"

git tag --points-at HEAD >"$before"
check "a tag an earlier run pushed and never published" "v0.2.0" "$("$script" "$before")"

echo v0.2.0 >>"$RELEASED"
check "a tag an earlier run pushed and published" "" "$("$script" "$before")"
succeeds "a tag an earlier run pushed and published"

git tag v0.2.1
check "a new tag beside an old one" "v0.2.1" "$("$script" "$before")"

if [ "$failures" -gt 0 ]; then
    exit 1
fi
echo "new-release-tag: every case matches"
