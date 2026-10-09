#!/usr/bin/env bash
# warm-go-proxy.test.sh — warm-go-proxy.sh asks the proxy for the version's
# .info under the module's own path, and refuses a tag that is no version.
#
# A file:// proxy stands in for proxy.golang.org: curl reads the .info the
# protocol names, so a wrong path fails here as it would against the proxy.
#
# Runs from `task verify-all` — no Docker, no network.

set -euo pipefail

script="$(git rev-parse --show-toplevel)/.github/scripts/warm-go-proxy.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

readonly REPO=oleg-tkachuk/limes
readonly VERSION=v0.3.0
mkdir -p "$work/proxy/github.com/$REPO/@v"
echo '{"Version":"v0.3.0"}' >"$work/proxy/github.com/$REPO/@v/$VERSION.info"
export GITHUB_REPOSITORY="$REPO" GOPROXY_URL="file://$work/proxy"

failures=0
fail() { printf 'FAIL %s\n' "$1"; failures=$((failures + 1)); }

got="$("$script" "$VERSION")" || fail "a published version"
[[ "$got" == *'"Version":"v0.3.0"'* ]] || fail "the .info read back: $got"

for tag in "" "0.3.0" "v0.3" "sdk/go/v0.3.0" "v0.3.0-rc.1"; do
    if "$script" "$tag" >/dev/null 2>&1; then
        fail "accepted ${tag:-an empty tag}"
    fi
done

if GITHUB_REPOSITORY=Oleg/limes "$script" "$VERSION" >/dev/null 2>&1; then
    fail "accepted an upper-case module path"
fi

if [ "$failures" -gt 0 ]; then
    exit 1
fi
echo "warm-go-proxy: every case matches"
