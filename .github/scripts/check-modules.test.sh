#!/usr/bin/env bash
# check-modules.test.sh — check-modules.sh runs a check over every module the
# release table holds, from each one's own range, skips a module with
# nothing to check from, and fails when any module's check did.
#
# Runs from `task verify-all` against a scratch repository — no network.

set -euo pipefail

root="$(git rev-parse --show-toplevel)"
scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT

# A repository holding the real check-modules.sh, check-range.sh and the
# module table, with api-break-check.sh and marker-check.sh as stubs that
# log their arguments and fail for the module named in $FAIL_MODULE.
git -C "$scratch" init --quiet --initial-branch=main
git -C "$scratch" config user.name test
git -C "$scratch" config user.email test@example.com
mkdir -p "$scratch/.github/scripts" "$scratch/.github/release"
cp "$root/.github/scripts/check-modules.sh" "$root/.github/scripts/check-range.sh" \
    "$root/.github/scripts/breaking-markers.sh" "$scratch/.github/scripts/"
cp "$root/.github/release/modules.cjs" "$root/.github/release/module-commits.cjs" "$scratch/.github/release/"
for stub in api-break-check marker-check; do
    cat >"$scratch/.github/scripts/$stub.sh" <<STUB
#!/usr/bin/env bash
printf '$stub %s\n' "\$*" >>"\$CALLS"
[ "\$3" != "\${FAIL_MODULE:-}" ]
STUB
    chmod +x "$scratch/.github/scripts/$stub.sh"
done
git -C "$scratch" add -A
git -C "$scratch" commit --quiet -m "feat: start"
first="$(git -C "$scratch" rev-parse HEAD)"
git -C "$scratch" tag v0.1.0
git -C "$scratch" commit --quiet --allow-empty -m "fix: later"
later="$(git -C "$scratch" rev-parse HEAD)"

script="$scratch/.github/scripts/check-modules.sh"
export CALLS="$scratch/calls"

failures=0
# expect name, want status, want calls, args...
expect() {
    local name="$1" want_status="$2" want_calls="$3"
    shift 3
    : >"$CALLS"
    local status=0
    (cd "$scratch" && "$script" "$@" >/dev/null 2>&1) || status=$?
    local calls
    calls="$(cat "$CALLS")"
    if [ "$status" != "$want_status" ] || [ "$calls" != "$want_calls" ]; then
        printf 'FAIL %s:\n  want %s %q\n  got  %s %q\n' "$name" "$want_status" "$want_calls" "$status" "$calls"
        failures=$((failures + 1))
    fi
}

expect "a push, the nested modules never released" 0 "api-break-check v0.1.0 $later limes" \
    api push "" "" "$later"
git -C "$scratch" tag redisreplay/v0.1.0 "$first"
expect "a push, each module from its own tag, one never released" 0 \
    "$(printf 'marker-check v0.1.0 %s limes\nmarker-check redisreplay/v0.1.0 %s redisreplay' "$later" "$later")" \
    markers push "" "" "$later"
# Every module of the table, in its order, for a pull request.
pr_calls="$(printf 'api-break-check %s %s limes\napi-break-check %s %s redisreplay\napi-break-check %s %s pgstore\napi-break-check %s %s otelmetric' \
    "$first" "$later" "$first" "$later" "$first" "$later" "$first" "$later")"
expect "a pull request" 0 "$pr_calls" api pull_request "$first" "$later" "$later"
FAIL_MODULE=limes expect "one module failing, the others still checked" 1 "$pr_calls" \
    api pull_request "$first" "$later" "$later"
expect "an unknown check" 2 "" bogus push "" "" "$later"

if [ "$failures" -gt 0 ]; then
    exit 1
fi
echo "check-modules: every case matches"
