# shellcheck shell=bash
# breaking-markers.sh — how a commit marks itself breaking, and which release
# line a change builds on, for api-break-check.sh and marker-check.sh.
#
# Usage: source "$(dirname "${BASH_SOURCE[0]}")/breaking-markers.sh"
#        select_module <name>     # the root module until called
#
# Every answer is about one module of the repository, as the release scopes
# it in .github/release/modules.cjs: the commits that touch it, the tags of
# its own release line, the rules it releases by. A checkout without that
# file is one module, the root, which every commit touches.

# The subject marker and the footer Conventional Commits 1.0 define, as the
# release's commit parser reads them: either case, any word as the type, and
# the footer after leading spaces, a | or a *, followed by a colon or a space,
# on any line but the subject. The type is ASCII, as the parser's \w is; a
# scope runs to the last ) before the !, as its (.*) does.
# Anything it reads as breaking, this must too.
readonly SUBJECT_MARK='^[A-Za-z0-9_]*(\(.*\))?!: '
readonly FOOTER_MARK='^[[:space:]|*]*BREAKING[ -]CHANGE[:[:space:]]'

# The first major whose release line keeps its API: from it on, a break
# would tag a major the Go proxy ignores without a new module path.
readonly STABLE_MAJOR=1

# The module table the release reads, and the root module's name in it.
readonly MODULES_CONFIG=.github/release/modules.cjs
readonly ROOT_MODULE=limes
# The release's own filter of the commits that touch a module, run as a
# command so the two can never count differently.
MODULE_COMMITS="$(cd "$(dirname "${BASH_SOURCE[0]}")/../release" && pwd)/module-commits.cjs"
readonly MODULE_COMMITS

# The module every function below answers for: select_module sets them.
MODULE_NAME="$ROOT_MODULE"
MODULE_DIR=""
MODULE_PREFIX=""

# modules_config prints the path of the checkout's module table, or nothing
# when it has none.
modules_config() {
    local path
    path="$(git rev-parse --show-toplevel)/$MODULES_CONFIG"
    [ ! -f "$path" ] || printf '%s\n' "$path"
}

# select_module makes <name> the module the functions below answer for: its
# directory and its tag prefix, from the checkout's module table. A module
# the table does not hold is an error.
select_module() {
    local config scope
    MODULE_NAME="$1"
    config="$(modules_config)"
    if [ -z "$config" ]; then
        [ "$1" = "$ROOT_MODULE" ] || { echo "no $MODULES_CONFIG, so no module $1" >&2; return 1; }
        MODULE_DIR="" MODULE_PREFIX=""
        return 0
    fi
    scope="$(node -e '
        const m = require(process.argv[1]);
        const cfg = m.configFor(process.argv[2]);
        console.log(cfg.scope.dir);
        console.log(m.tagPrefix(cfg.scope.dir));
    ' "$config" "$1")" || return 1
    MODULE_DIR="$(sed -n 1p <<<"$scope")"
    MODULE_PREFIX="$(sed -n 2p <<<"$scope")"
}

# released_modules prints the name of every module the checkout releases, one
# a line: the root module alone without a module table.
released_modules() {
    local config
    config="$(modules_config)"
    if [ -z "$config" ]; then
        printf '%s\n' "$ROOT_MODULE"
        return 0
    fi
    node -e 'console.log(Object.keys(require(process.argv[1]).MODULES).join("\n"))' "$config"
}

# module_commits prints the commits in <from>..<to> that touch the module.
module_commits() {
    local config
    config="$(modules_config)"
    if [ -z "$config" ]; then
        git log --format=%H "$1..$2"
        return 0
    fi
    git log --format=%H "$1..$2" | node "$MODULE_COMMITS" "$config" "$MODULE_NAME"
}

# marks_breaking reports whether a commit in the range <from>..<to> that
# touches the module marks itself breaking: only those reach its release.
marks_breaking() {
    local hash message
    while IFS= read -r hash; do
        message="$(git log -1 --format=%B "$hash")"
        if grep -qiE "$SUBJECT_MARK" <<<"$(head -n 1 <<<"$message")" ||
            grep -qiE "$FOOTER_MARK" <<<"$(tail -n +2 <<<"$message")"; then
            return 0
        fi
    done < <(module_commits "$1" "$2")
    return 1
}

# release_line prints the newest release tag of the module reachable from
# <rev>, empty before any.
release_line() {
    git tag --merged "$1" --list "${MODULE_PREFIX}v[0-9]*" --sort=-v:refname | head -n 1
}

# on_stable_line reports whether the release line <tag> keeps its API.
on_stable_line() {
    local version="${1#"$MODULE_PREFIX"}"
    local major="${version#v}"
    [ -n "$1" ] && [ "${major%%.*}" -ge "$STABLE_MAJOR" ]
}

# The rule that makes a break a minor before v1.0.0. The change that cuts a
# module's v1.0.0 removes it from that module's rules; the tag follows only
# once the release has run, so a change built on the commit without the rule
# is on the stable line already, tag or no tag. A revision with the module
# table is asked through it (breaksAreMinor); one from before the table
# holds the rule in the release config, for the root module only.
readonly RELEASE_CONFIG=.github/release/release.config.cjs
readonly PRE_STABLE_RULE=BREAKING_IS_A_MINOR_BEFORE_1_0
# What the node below exits with for each answer. A table that fails to load
# exits otherwise, and is read as releasing majors: the checks then refuse a
# marker, loudly, rather than let one through on a guess.
readonly BREAKS_ARE_MINOR=0 BREAKS_ARE_MAJOR=3 MODULE_UNKNOWN=2

# releases_majors reports whether the module, as the release config at
# <rev> holds it, releases a break as a major. A revision with no config, or
# none for the module, says nothing either way.
releases_majors() {
    local copy status config
    if git cat-file -e "$1:$MODULES_CONFIG" 2>/dev/null; then
        # A copy of its own: the table requires nothing, so it loads alone.
        copy="$(mktemp -d)"
        git show "$1:$MODULES_CONFIG" >"$copy/modules.cjs"
        status=0
        node -e '
            const m = require(process.argv[1]);
            const [name, minor, major, unknown] = process.argv.slice(2);
            if (!m.MODULES[name]) process.exit(Number(unknown));
            process.exit(Number(m.breaksAreMinor(name) ? minor : major));
        ' "$copy/modules.cjs" "$MODULE_NAME" "$BREAKS_ARE_MINOR" "$BREAKS_ARE_MAJOR" "$MODULE_UNKNOWN" || status=$?
        rm -rf "$copy"
        [ "$status" -ne "$BREAKS_ARE_MINOR" ] && [ "$status" -ne "$MODULE_UNKNOWN" ]
        return
    fi
    [ "$MODULE_NAME" = "$ROOT_MODULE" ] || return 1
    config="$(git show "$1:$RELEASE_CONFIG" 2>/dev/null)" || return 1
    ! grep -qF "$PRE_STABLE_RULE" <<<"$config"
}

# stable_at reports whether a change built on <rev> must keep the API: a
# stable release line is tagged at or below it, or its release config
# already releases breaks as majors.
stable_at() {
    on_stable_line "$(release_line "$1")" || releases_majors "$1"
}

# title_marks_breaking reports whether a pull request title would mark its
# merge commit breaking: the title becomes the commit's body, where the
# release's parser reads a BREAKING CHANGE footer.
title_marks_breaking() {
    grep -qiE "$FOOTER_MARK" <<<"$1"
}
