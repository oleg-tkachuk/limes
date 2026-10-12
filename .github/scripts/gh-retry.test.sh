#!/usr/bin/env bash
# gh-retry.test.sh — gh_retry tries gh again after a transient failure, up to
# its limit, and returns any other failure at once.
#
# A fake gh fails the first FAIL_TIMES calls with FAIL_WITH on stderr, then
# prints "ok"; it counts its calls in CALLS.
#
# Runs from `task verify-all` — no Docker, no network.

set -euo pipefail

root="$(git rev-parse --show-toplevel)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

cat >"$work/gh" <<'GH'
#!/usr/bin/env bash
n=$(($(cat "$CALLS") + 1))
echo "$n" >"$CALLS"
if [ "$n" -le "$FAIL_TIMES" ]; then
    echo "$FAIL_WITH" >&2
    exit 1
fi
echo ok
GH
chmod +x "$work/gh"
export PATH="$work:$PATH" CALLS="$work/calls" GH_RETRY_DELAY_SECONDS=0
# shellcheck source=.github/scripts/gh-retry.sh
source "$root/.github/scripts/gh-retry.sh"

failures=0
check() { # name, fail times, stderr, want (ok|fail), want calls
    local got=ok out
    echo 0 >"$CALLS"
    out="$(FAIL_TIMES="$2" FAIL_WITH="$3" gh_retry api x 2>/dev/null)" || got=fail
    if [ "$got" != "$4" ] || [ "$(cat "$CALLS")" != "$5" ] || { [ "$got" = ok ] && [ "$out" != ok ]; }; then
        printf 'FAIL %s: got %s after %s calls (output %q), want %s after %s\n' "$1" "$got" "$(cat "$CALLS")" "$out" "$4" "$5"
        failures=$((failures + 1))
    fi
}

check "first try" 0 "" ok 1
check "a 502 then success" 1 "gh: Server Error (HTTP 502)" ok 2
check "a 504 twice then success" 2 "HTTP 504: Gateway Timeout" ok 3
check "a reset connection" 1 "read: connection reset by peer" ok 2
check "5xx past the limit" 9 "HTTP 503" fail "$GH_RETRY_ATTEMPTS"
check "a 404 is the answer" 1 "gh: Not Found (HTTP 404)" fail 1
check "a 422 is the answer" 1 "HTTP 422: Unprocessable Entity" fail 1

if [ "$failures" -gt 0 ]; then
    exit 1
fi
echo "gh-retry: every case matches"
