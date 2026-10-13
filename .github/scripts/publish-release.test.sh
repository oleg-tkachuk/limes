#!/usr/bin/env bash
# publish-release.test.sh — publish-release.sh publishes a module's release
# with the previous tag of the same module as the start of its notes, and
# never marks a nested module's release, or one left behind, as Latest.
#
# Runs from `task verify-all` — no Docker, no network.

set -euo pipefail

script="$(git rev-parse --show-toplevel)/.github/scripts/publish-release.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# gh stands in for GitHub: `gh release view <tag>` succeeds for the tags in
# $RELEASED, and `gh release create` records its arguments, one call a line,
# in $CREATED and counts the tag as released.
mkdir "$work/bin"
cat >"$work/bin/gh" <<'GH'
#!/usr/bin/env bash
case "$1 $2" in
"release view") grep -qxF "$3" "$RELEASED" ;;
"release create")
    shift 2
    printf '%s\n' "$*" >>"$CREATED"
    echo "$1" >>"$RELEASED"
    ;;
*) exit 1 ;;
esac
GH
chmod +x "$work/bin/gh"
export PATH="$work/bin:$PATH" RELEASED="$work/released" CREATED="$work/created"
: >"$RELEASED"

# A file:// proxy holding every version these cases release, so the warm-up
# succeeds without a network.
readonly REPO=oleg-tkachuk/limes
export GITHUB_REPOSITORY="$REPO" GOPROXY_URL="file://$work/proxy"
for path in "$REPO/@v/v0.2.0" "$REPO/sub/@v/v0.1.0" "$REPO/sub/@v/v0.2.0"; do
    mkdir -p "$work/proxy/github.com/$(dirname "$path")"
    echo '{}' >"$work/proxy/github.com/$path.info"
done
export RELEASE_NOTES_FILE="$work/notes.md"
echo "the notes" >"$RELEASE_NOTES_FILE"

cd "$work"
git init -q repo && cd repo
commit() { git -c user.name=t -c user.email=t@example.invalid commit -q --allow-empty -m "$1"; }
before="$work/before"

failures=0
# created checks the arguments of the one release this step created.
created() { # name, want
    local got
    got="$(cat "$CREATED" 2>/dev/null || true)"
    if [ "$2" != "$got" ]; then
        printf 'FAIL %s:\n  want %q\n  got  %q\n' "$1" "$2" "$got"
        failures=$((failures + 1))
    fi
    : >"$CREATED"
}
run() { "$script" "$@" >/dev/null; }

readonly BASE="--repo $REPO --generate-notes --verify-tag"
readonly NOTES="--notes-file $RELEASE_NOTES_FILE"

commit one
git tag v0.1.0
echo v0.1.0 >>"$RELEASED"
commit two
git tag --points-at HEAD >"$before"
run new "$before"
created "no tag pushed" ""

git tag v0.2.0
run new "$before"
created "a root release" "v0.2.0 $BASE --notes-start-tag v0.1.0 $NOTES"

git tag --points-at HEAD >"$before"
git tag sub/v0.1.0
run new "$before" sub/
created "a nested module's first release" "sub/v0.1.0 $BASE --latest=false $NOTES"

commit three
git tag --points-at HEAD >"$before"
git tag sub/v0.2.0
run new "$before" sub/
created "a nested module's release" "sub/v0.2.0 $BASE --notes-start-tag sub/v0.1.0 --latest=false $NOTES"

commit four
git tag v0.3.0 # its run failed before publishing
git tag sub/v0.3.0
commit five
run left-behind
created "a root tag left behind" "v0.3.0 $BASE --notes-start-tag v0.2.0 --latest=false"
run left-behind sub/
created "a nested tag left behind" "sub/v0.3.0 $BASE --notes-start-tag sub/v0.2.0 --latest=false"

if "$script" bogus >/dev/null 2>&1; then
    printf 'FAIL an unknown mode was accepted\n'
    failures=$((failures + 1))
fi

if [ "$failures" -gt 0 ]; then
    exit 1
fi
echo "publish-release: every case matches"
