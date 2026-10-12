# shellcheck shell=bash
# breaking-markers.sh — how a commit marks itself breaking, and which release
# line a change builds on, for api-break-check.sh and marker-check.sh.
#
# Usage: source "$(dirname "${BASH_SOURCE[0]}")/breaking-markers.sh"

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

# marks_breaking reports whether a commit in the range <from>..<to> marks
# itself breaking.
marks_breaking() {
    local message
    while IFS= read -r -d '' message; do
        if grep -qiE "$SUBJECT_MARK" <<<"$(head -n 1 <<<"$message")" ||
            grep -qiE "$FOOTER_MARK" <<<"$(tail -n +2 <<<"$message")"; then
            return 0
        fi
    done < <(git log -z --format=%B "$1..$2")
    return 1
}

# release_line prints the newest release tag reachable from <rev>, empty
# before any.
release_line() {
    git tag --merged "$1" --list 'v[0-9]*' --sort=-v:refname | head -n 1
}

# on_stable_line reports whether the release line <tag> keeps its API.
on_stable_line() {
    local major="${1#v}"
    [ -n "$1" ] && [ "${major%%.*}" -ge "$STABLE_MAJOR" ]
}

# The release config, and the rule in it that makes a break a minor before
# v1.0.0. The change that cuts v1.0.0 removes the rule; the tag follows only
# once the release has run, so a change built on the commit without the rule
# is on the stable line already, tag or no tag.
readonly RELEASE_CONFIG=.github/release/release.config.cjs
readonly PRE_STABLE_RULE=BREAKING_IS_A_MINOR_BEFORE_1_0

# releases_majors reports whether the release config at <rev> releases a
# break as a major. A revision with no config says nothing either way.
releases_majors() {
    local config
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
