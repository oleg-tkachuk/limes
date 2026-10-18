#!/usr/bin/env bash
# marker-check.test.sh — marker-check.sh lets breaking markers through before
# v1.0.0, when the change writes its migration entry, and refuses every one
# from then on, a documentation-only change's included, in every spelling the
# release's commit parser reads.
#
# Runs from `task verify-all` against a scratch repository — no network.

set -euo pipefail

root="$(git rev-parse --show-toplevel)"
script="$root/.github/scripts/marker-check.sh"

scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT

git -C "$scratch" init --quiet --initial-branch=main
git -C "$scratch" config user.name test
git -C "$scratch" config user.email test@example.com
git -C "$scratch" commit --quiet --allow-empty -m "feat: start"

failures=0
# check name, want (pass|fail), commit messages...
check() {
    local name="$1" want="$2"
    shift 2
    git -C "$scratch" checkout --quiet -B case main
    local message
    for message in "$@"; do
        printf '%s\n' "$message" >>"$scratch/README.md"
        git -C "$scratch" add README.md
        if [ -n "${MIGRATION:-}" ]; then
            mkdir -p "$scratch/docs"
            printf '%s\n' "$message" >>"$scratch/docs/migration.md"
            git -C "$scratch" add docs/migration.md
        fi
        git -C "$scratch" commit --quiet -m "$message"
    done
    local got=pass
    (cd "$scratch" && PR_TITLE="${TITLE:-}" "$script" main case >/dev/null 2>&1) || got=fail
    if [ "$want" != "$got" ]; then
        printf 'FAIL %s: want %s, got %s\n' "$name" "$want" "$got"
        failures=$((failures + 1))
    fi
}

# Before any release, and on 0.x, a marker cuts a minor: it passes when the
# change writes its migration entry.
MIGRATION=1 check "no release, a marker" pass "feat!: change"
git -C "$scratch" tag v0.9.0 main
MIGRATION=1 check "on v0, a marker" pass "feat!: change"
MIGRATION=1 check "on v0, a footer" pass "$(printf 'fix: x\n\nBREAKING CHANGE: gone')"
check "on v0, a marker without a migration entry" fail "feat!: change"
check "on v0, a footer without a migration entry" fail "$(printf 'fix: x\n\nBREAKING CHANGE: gone')"
MIGRATION=1 check "on v0, a marker in an earlier commit" pass "feat!: change" "docs: more"
check "on v0, no marker" pass "docs: explain"

# A title that reads as a footer in the merge commit is refused on any line.
TITLE="Breaking change: drop X" check "on v0, a footer as the title" fail "docs: explain"
TITLE="* BREAKING CHANGE: drop X" check "on v0, a listed footer as the title" fail "docs: explain"
TITLE="docs: explain the breaking change" check "on v0, the words in a title" pass "docs: explain"

# The same, with the module table release.config.cjs reads its rules from:
# the root module's own rules decide, not the text of release.config.cjs.
git -C "$scratch" checkout --quiet main
mkdir -p "$scratch/.github/release"
cp "$root/.github/release/modules.cjs" "$root/.github/release/release.config.cjs" "$scratch/.github/release/"
git -C "$scratch" add .github
git -C "$scratch" commit --quiet -m "build: release from the module table"
MIGRATION=1 check "on v0, the pre-1.0 rule in the module table" pass "feat!: change"
git -C "$scratch" checkout --quiet main
# The root's rules are the one list that holds the rule on a line of its own.
sed -i.bak '/^      BREAKING_IS_A_MINOR_BEFORE_1_0,$/d' "$scratch/.github/release/modules.cjs"
rm -f "$scratch/.github/release/modules.cjs.bak"
git -C "$scratch" commit --quiet -am "build!: release root breaks as majors"
check "the root's pre-1.0 rule gone from the module table" fail "feat!: change"
git -C "$scratch" checkout --quiet main
git -C "$scratch" rm --quiet -r .github
git -C "$scratch" commit --quiet -m "build: drop the module table"

# The release config decides before the tag does: with the pre-1.0 rule a
# marker cuts a minor; once the change cutting v1.0.0 removed it, a change
# built on that commit is on the stable line, though v1.0.0 is not tagged yet.
git -C "$scratch" checkout --quiet main
config="$scratch/.github/release/release.config.cjs"
mkdir -p "$(dirname "$config")"
printf 'const BREAKING_IS_A_MINOR_BEFORE_1_0 = {};\n' >"$config"
git -C "$scratch" add .github
git -C "$scratch" commit --quiet -m "build: release breaks as minors"
MIGRATION=1 check "on v0, the pre-1.0 rule in force" pass "feat!: change"
git -C "$scratch" checkout --quiet main
printf 'module.exports = {};\n' >"$config"
git -C "$scratch" commit --quiet -am "build!: release breaks as majors"
check "the rule gone, v1.0.0 not tagged yet" fail "feat!: change"
check "the rule gone, no marker" pass "feat: add"

# From v1.0.0 on, every marker the parser reads is refused.
git -C "$scratch" tag v1.0.0 main
check "on v1, no marker" pass "docs: explain" "feat: add"
check "on v1, a subject marker" fail "feat!: change"
check "on v1, a scoped marker" fail "fix(api)!: change"
check "on v1, a capitalised type" fail "Docs!: change"
check "on v1, a footer" fail "$(printf 'docs: explain\n\nBREAKING CHANGE: gone')"
check "on v1, a footer in another case" fail "$(printf 'docs: explain\n\nbreaking change: gone')"
check "on v1, a marker in an earlier commit" fail "docs!: explain" "docs: more"
check "on v1, a ! outside the type" pass "fix: a!: b"

# A repository of two modules, as the release scopes them: a marker counts
# for the module whose files its commit touches, and each module is on its
# own release line.
nested="$scratch/nested"
readonly NESTED=redisreplay
git -C "$scratch" init --quiet --initial-branch=main "$nested"
git -C "$nested" config user.name test
git -C "$nested" config user.email test@example.com
mkdir -p "$nested/.github/release" "$nested/$NESTED"
cp "$root/.github/release/modules.cjs" "$root/.github/release/release.config.cjs" "$nested/.github/release/"
: >"$nested/README.md"
: >"$nested/$NESTED/README.md"
git -C "$nested" add -A
git -C "$nested" commit --quiet -m "feat: start"
git -C "$nested" tag v0.1.0
git -C "$nested" tag "$NESTED/v0.1.0"

# check_module name, want, module, path the commit touches, message
check_module() {
    local name="$1" want="$2" module="$3" path="$4" message="$5"
    git -C "$nested" checkout --quiet -B case main
    printf '%s\n' "$message" >>"$nested/$path"
    [ -z "${MIGRATION:-}" ] || mkdir -p "$nested/docs"
    [ -z "${MIGRATION:-}" ] || printf '%s\n' "$message" >>"$nested/docs/migration.md"
    git -C "$nested" add -A
    git -C "$nested" commit --quiet -m "$message"
    local got=pass
    (cd "$nested" && "$script" main case "$module" >/dev/null 2>&1) || got=fail
    if [ "$want" != "$got" ]; then
        printf 'FAIL %s: want %s, got %s\n' "$name" "$want" "$got"
        failures=$((failures + 1))
    fi
}

check_module "a nested marker with no migration entry" fail "$NESTED" "$NESTED/README.md" "feat($NESTED)!: change"
check_module "a nested marker, checked for the root" pass limes "$NESTED/README.md" "feat($NESTED)!: change"
check_module "a root marker, checked for the nested module" pass "$NESTED" README.md "feat!: change"
MIGRATION=1 check_module "a nested marker with its migration entry" pass "$NESTED" "$NESTED/README.md" "feat($NESTED)!: change"
git -C "$nested" checkout --quiet main
git -C "$nested" tag "$NESTED/v1.0.0"
check_module "a nested marker on its stable line" fail "$NESTED" "$NESTED/README.md" "feat($NESTED)!: change"
MIGRATION=1 check_module "a root marker beside the nested stable line" pass limes README.md "feat!: change"
# The release analyzer failing to run is no range without a marker: the
# check fails rather than passing a marker it never read. A node in front of
# the real one fails module-commits.cjs alone.
broken="$scratch/broken-node"
mkdir -p "$broken"
real_node="$(command -v node)"
cat >"$broken/node" <<NODE
#!/usr/bin/env bash
for arg in "\$@"; do
    case "\$arg" in *module-commits.cjs) exit 3 ;; esac
done
exec "$real_node" "\$@"
NODE
chmod +x "$broken/node"
git -C "$nested" checkout --quiet -B case main
printf 'x\n' >>"$nested/README.md"
git -C "$nested" commit --quiet -am "feat!: change"
if (cd "$nested" && PATH="$broken:$PATH" "$script" main case limes >/dev/null 2>&1); then
    printf 'FAIL a marker check whose analyzer could not run passed\n'
    failures=$((failures + 1))
fi
git -C "$nested" checkout --quiet main

if (cd "$nested" && "$script" main main no-such-module >/dev/null 2>&1); then
    printf 'FAIL a module the table does not hold was checked\n'
    failures=$((failures + 1))
fi

if [ "$failures" -gt 0 ]; then
    exit 1
fi
echo "marker-check: every case matches"
