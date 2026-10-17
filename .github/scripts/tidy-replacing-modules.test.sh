#!/usr/bin/env bash
# tidy-replacing-modules.test.sh — tidy-replacing-modules.sh tidies exactly
# the nested modules that replace the root module in, each after the nested
# modules it replaces in.
#
# A repository of go.mod files stands in for this one, and a go that records
# each tidy stands in for the tidy itself; go mod edit runs for real, so the
# replace is read the way the script reads it here.
#
# Runs from `task verify-all` — no Docker, no network.

set -euo pipefail

script="$(git rev-parse --show-toplevel)/.github/scripts/tidy-replacing-modules.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

readonly ROOT_MODULE=example.com/root
readonly OTHER_MODULE=example.com/other

repo="$work/repo"
mkdir -p "$repo"
git -C "$repo" init -q

modfile() { # dir, module path, body
    mkdir -p "$repo/$1"
    printf 'module %s\n\ngo 1.26\n%s' "$2" "${3:-}" >"$repo/$1/go.mod"
}

modfile . "$ROOT_MODULE"
# store and the "space dir" both replace the root in; app replaces store too,
# so it comes after store although it sorts first.
modfile store "$ROOT_MODULE/store" "
require $ROOT_MODULE v0.1.0

replace $ROOT_MODULE => ../
"
modfile app "$ROOT_MODULE/app" "
require (
	$ROOT_MODULE v0.1.0
	$ROOT_MODULE/store v0.1.0
)

replace (
	$ROOT_MODULE => ../
	$ROOT_MODULE/store => ../store
)
"
modfile "space dir" "$ROOT_MODULE/spaced" "
replace $ROOT_MODULE => ../
"
# released requires the root by version, the way redisreplay does.
modfile released "$ROOT_MODULE/released" "
require $ROOT_MODULE v0.1.0
"
# vendored replaces the root with another version, not a directory.
modfile vendored "$ROOT_MODULE/vendored" "
replace $ROOT_MODULE => $ROOT_MODULE v0.0.9
"
# tools replaces another module in, not the root.
modfile tools "$ROOT_MODULE/tools" "
replace $OTHER_MODULE => ../other
"
# untracked replaces the root in but git does not track it.
git -C "$repo" add .
modfile untracked "$ROOT_MODULE/untracked" "
replace $ROOT_MODULE => ../
"

stub="$work/go"
log="$work/tidied"
cat >"$stub" <<EOF
#!/usr/bin/env bash
if [ "\$1" = -C ] && [ "\$3 \$4" = "mod tidy" ]; then
    printf '%s\n' "\$2" >>"$log"
    exit 0
fi
exec go "\$@"
EOF
chmod +x "$stub"

failures=0
fail() { printf 'FAIL %s\n' "$1"; failures=$((failures + 1)); }

: >"$log"
(cd "$repo/store" && GO="$stub" "$script" >/dev/null) || fail "the script failed"
want="$(printf '%s\n' "space dir" store app)"
got="$(cat "$log")"
[ "$got" = "$want" ] || fail "tidied, in order: want [$want], got [$got]"

# A repository where no nested module replaces the root in tidies nothing.
git -C "$repo" rm -q -r --cached store app "space dir"
: >"$log"
(cd "$repo" && GO="$stub" "$script" >/dev/null) || fail "the script failed with nothing to tidy"
[ ! -s "$log" ] || fail "tidied with no replacing module: $(cat "$log")"

if "$script" extra >/dev/null 2>&1; then
    fail "accepted an argument"
fi

if [ "$failures" -gt 0 ]; then
    exit 1
fi
echo "tidy-replacing-modules: every case matches"
