#!/usr/bin/env bash
# Tests for go-test-changed.sh, each against a throwaway repository of modules:
#   .                     the root module, standalone (only with_root)
#   backend/a             standalone
#   backend/b             requires backend/a
#   backend/c             standalone
#   backend/d             requires backend/b
#   sdk/go                requires sdk/go/internal/kit
#   sdk/go/internal/kit   nested inside sdk/go
#   backend/tools         pins tools only: a module with no package to test
#   testdata/fixture      a go.mod under testdata, which is not a module
# Run: bash scripts/hooks/go-test-changed_test.sh
set -euo pipefail

# A hook runs this with git's own variables set; the fixtures' git commands
# must act on the fixtures, not on this repository.
unset GIT_DIR GIT_INDEX_FILE GIT_WORK_TREE GIT_PREFIX

TMP_ROOT="$(mktemp -d)"
readonly TMP_ROOT
trap 'rm -rf "$TMP_ROOT"' EXIT

SCRIPT="$(cd "$(dirname "$0")" && pwd)/go-test-changed.sh"
readonly SCRIPT
REAL_GO="$(command -v go)"
readonly REAL_GO
export REAL_GO
readonly MODULE_PREFIX="example.test"
failures=0

# The go stub logs, for each `go test`, the module directory relative to the
# fixture root and the flags it was given; everything else goes to go.
STUB_GO="$TMP_ROOT/go-stub"
readonly STUB_GO
cat > "$STUB_GO" <<'EOF'
#!/usr/bin/env bash
if [ "$1" = test ]; then
  rel="${PWD#"$FIXTURE_ROOT"}"
  rel="${rel#/}"
  shift
  echo "${rel:-.} $*" >> "$GO_TEST_LOG"
  exit "${GO_TEST_EXIT:-0}"
fi
if [ "$1" = list ] && [ -n "${GO_LIST_FAILS:-}" ]; then
  echo "go: updates to go.mod needed" >&2
  exit 1
fi
exec "$REAL_GO" "$@"
EOF
chmod +x "$STUB_GO"

# module writes <dir>/go.mod requiring the modules in the other directories,
# and one package for its tests to run in.
module() {
  local root="$1" dir="$2"
  shift 2
  mkdir -p "$root/$dir"
  {
    echo "module $MODULE_PREFIX/$dir"
    echo
    echo "go 1.24"
    for dep in "$@"; do
      echo
      echo "require $MODULE_PREFIX/$dep v0.0.0"
    done
  } > "$root/$dir/go.mod"
  echo "package $(basename "$dir")" > "$root/$dir/doc.go"
}

# tools_module writes a module whose only file is excluded by a build tag, as
# a module pinning tool versions is.
tools_module() {
  local root="$1" dir="$2"
  mkdir -p "$root/$dir"
  printf 'module %s/%s\n\ngo 1.24\n' "$MODULE_PREFIX" "$dir" > "$root/$dir/go.mod"
  printf '//go:build tools\n\npackage tools\n' > "$root/$dir/tools.go"
}

# new_repo builds the fixture; with_root adds a root module.
new_repo() {
  local root
  root="$(mktemp -d "$TMP_ROOT/repo.XXXXXX")"
  if [ "${1:-}" = with_root ]; then
    printf 'module %s/root\n\ngo 1.24\n' "$MODULE_PREFIX" > "$root/go.mod"
    echo "package root" > "$root/doc.go"
  fi
  module "$root" backend/a
  module "$root" backend/b backend/a
  module "$root" backend/c
  module "$root" backend/d backend/b
  module "$root" sdk/go sdk/go/internal/kit
  module "$root" sdk/go/internal/kit
  tools_module "$root" backend/tools
  module "$root" testdata/fixture
  git -C "$root" init -q
  git -C "$root" add -A
  echo "$root"
}

# run_hook runs the script in a fixture; extra env comes first as VAR=value.
run_hook() {
  local root="$1"
  shift
  FIXTURE_ROOT="$root" REPO_ROOT="$root" GO_CMD="$STUB_GO" GO_TEST_LOG="$root/tested.log" \
    "$SCRIPT" "$@"
}

# tested prints the module directories tested, space-separated, in order.
tested() {
  local layout="$1" root
  shift
  root="$(new_repo "$layout")"
  : > "$root/tested.log"
  run_hook "$root" "$@" >/dev/null
  cut -d' ' -f1 < "$root/tested.log" | tr '\n' ' ' | sed 's/ $//'
}

expect() {
  local name="$1" want="$2" got="$3"
  if [ "$want" = "$got" ]; then
    echo "ok   $name"
  else
    echo "FAIL $name: want '$want', got '$got'"
    return 1
  fi
}

check() {
  expect "$1" "$2" "$3" || failures=$((failures + 1))
}

check "a change to a standalone module tests only that module" \
  "backend/c" "$(tested no_root backend/c/x.go)"
check "a change to a module tests every module built on it, transitively" \
  "backend/a backend/b backend/d" "$(tested no_root backend/a/x.go)"
check "a change in the middle of the chain tests it and what is above it" \
  "backend/b backend/d" "$(tested no_root backend/b/x.go)"
check "a non-Go file counts: tests read fixtures and migrations" \
  "backend/c" "$(tested no_root backend/c/migrations/0001.sql)"
check "a go.mod bump counts" \
  "backend/c" "$(tested no_root backend/c/go.mod)"
check "a file belongs to the nearest module, not to the one around it" \
  "sdk/go sdk/go/internal/kit" "$(tested no_root sdk/go/internal/kit/x.go)"
check "the outer module's own change leaves the nested module alone" \
  "sdk/go" "$(tested no_root sdk/go/y.go)"
check "a workspace change tests every module" \
  "backend/a backend/b backend/c backend/d sdk/go sdk/go/internal/kit" "$(tested no_root go.work)"
check "several changed modules are each tested once" \
  "backend/a backend/b backend/c backend/d" "$(tested no_root backend/c/x.go backend/a/y.go backend/b/z.go)"
check "a change outside every module tests nothing" \
  "" "$(tested no_root docs/adr/0001.md backend/README.md)"
check "a sibling whose name prefixes another's is not mistaken for it" \
  "" "$(tested no_root backend/cc/x.go)"
check "a go.mod under testdata is not a module" \
  "" "$(tested no_root testdata/fixture/x.go)"
check "with a root module, a file outside the others belongs to the root" \
  "." "$(tested with_root docs/adr/0001.md testdata/fixture/x.go)"
check "with a root module, a nested module's change does not test the root" \
  "backend/c" "$(tested with_root backend/c/x.go)"

root="$(new_repo no_root)"
run_hook "$root" backend/c/x.go >/dev/null
check "go test runs one package and one test at a time by default" \
  "backend/c -p 1 -parallel 1 -count=1 ./..." "$(cat "$root/tested.log")"

root="$(new_repo no_root)"
GO_TEST_FLAGS="-short -race" run_hook "$root" backend/c/x.go >/dev/null
check "GO_TEST_FLAGS replaces the default flags" \
  "backend/c -short -race ./..." "$(cat "$root/tested.log")"

root="$(new_repo no_root)"
: > "$root/tested.log"
out="$(run_hook "$root" backend/tools/go.mod)" && got=pass || got=fail
check "a module with no package to test is skipped, not failed" pass "$got"
check "the skipped module is not tested" "" "$(cat "$root/tested.log")"
case "$out" in *"skipping backend/tools: no packages to test"*) got=said ;; *) got="$out" ;; esac
check "the skip says why" said "$got"

root="$(new_repo no_root)"
GO_LIST_FAILS=1 run_hook "$root" backend/c/x.go >/dev/null 2>&1
check "a module go list cannot load is tested, so the error shows, not skipped" \
  "backend/c" "$(cut -d' ' -f1 < "$root/tested.log")"

root="$(new_repo no_root)"
GO_TEST_EXIT=1 run_hook "$root" backend/c/x.go >/dev/null 2>&1 && got=pass || got=fail
check "a failing module fails the push" fail "$got"

root="$(new_repo no_root)"
out="$(run_hook "$root" backend/a/x.go)"
case "$out" in *"backend/b depends on backend/a"*) got=said ;; *) got="$out" ;; esac
check "the reason a dependent is tested is said" said "$got"

if [ "$failures" -gt 0 ]; then
  echo "$failures test(s) failed"
  exit 1
fi
