#!/usr/bin/env bash
# without-workflow-commands.test.sh — without-workflow-commands.sh stops
# workflow commands before the command's output and resumes them after it
# with the same fresh token, and exits with the command's status, also when
# the command fails.
#
# Runs from `task verify-all` — no network.

set -euo pipefail

root="$(git rev-parse --show-toplevel)"
script="$root/.github/scripts/without-workflow-commands.sh"

failures=0
fail() { printf 'FAIL %s\n' "$1"; failures=$((failures + 1)); }

# run name, want status, command...: checks the output's shape and the
# status, and leaves the token it resumed with in token.
token=""
run() {
    local name="$1" want="$2" out status=0
    shift 2
    out="$("$script" "$@")" || status=$?
    [ "$status" = "$want" ] || fail "$name: status $status, want $want"
    local first last
    first="$(head -n1 <<<"$out")"
    last="$(tail -n1 <<<"$out")"
    token="${first#::stop-commands::}"
    [[ $first == ::stop-commands::* && -n $token ]] || fail "$name: first line $first does not stop commands"
    [ "$last" = "::$token::" ] || fail "$name: last line $last does not resume with $token"
    grep -q -- '::error::from a commit' <<<"$out" || fail "$name: the command's output is missing"
}

run "a command that succeeds" 0 bash -c 'echo "::error::from a commit"'
first_token="$token"
run "a command that fails" 3 bash -c 'echo "::error::from a commit"; exit 3'
[ "$first_token" != "$token" ] || fail "two runs used the same token $token"

if "$script" >/dev/null 2>&1; then
    fail "no command: the script succeeded"
fi

if [ "$failures" -gt 0 ]; then
    exit 1
fi
echo "without-workflow-commands: every case matches"
