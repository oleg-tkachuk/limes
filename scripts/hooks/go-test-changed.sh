#!/usr/bin/env bash
# Run the Go tests of the modules a push changes, and of the modules built on
# them.
#
# Usage: go-test-changed.sh <changed file>...
# The files are repository-relative, as lefthook's {push_files} gives them.
#
# The modules are the go.mod files git tracks, outside testdata/ and vendor/.
# A file belongs to the nearest module above it, so a module nested inside
# another is its own module, and with a root module every other file is the
# root's. A module counts as changed when any file of it changed, not only
# *.go: its tests also read migrations, fixtures and embedded files, and a
# go.mod bump changes what they build against. A change to the workspace
# (go.work, go.work.sum) re-tests every module. A module whose go.mod requires
# a changed module is re-tested too, transitively: a change to a shared
# library is a change to everything built on it.
#
# The same script is used across several repositories; keep the copies equal.
#
# Inputs:
#   REPO_ROOT      the repository root. Defaults to git's top level.
#   GO_CMD         the go binary. Defaults to `go`; tests put a stub here.
#   GO_TEST_FLAGS  flags for each `go test`, replacing the default of one
#                  package and one test at a time, so a push does not compete
#                  with whatever else the machine is building.
set -euo pipefail

: "${REPO_ROOT:=$(git rev-parse --show-toplevel)}"
: "${GO_CMD:=go}"
: "${GO_TEST_FLAGS:=-p 1 -parallel 1 -count=1}"

readonly WORKSPACE_FILES=" go.work go.work.sum "
readonly ROOT_MODULE="."
readonly LOG_PREFIX="go-test-changed:"

read -r -a test_flags <<<"$GO_TEST_FLAGS"

cd "$REPO_ROOT"

# Index-aligned: dirs[i] is a module's directory, paths[i] its module path,
# requires[i] the module paths its go.mod requires, one per line. Plain arrays,
# because the bash macOS ships has no associative ones.
dirs=()
paths=()
requires=()
while IFS= read -r mod; do
  dir="$(dirname "$mod")"
  json="$(cd "$dir" && "$GO_CMD" mod edit -json)"
  dirs+=("$dir")
  paths+=("$(jq -r '.Module.Path' <<<"$json")")
  requires+=("$(jq -r '.Require[]?.Path' <<<"$json")")
done < <(git ls-files -- 'go.mod' '**/go.mod' ':(exclude,glob)**/testdata/**' ':(exclude,glob)**/vendor/**')

if [ "${#dirs[@]}" -eq 0 ]; then
  echo "$LOG_PREFIX no Go modules in this repository; nothing to test"
  exit 0
fi

# selected holds the chosen module directories, space-delimited and padded so
# a membership test cannot match one name inside another.
selected=" "
is_selected() { case "$selected" in *" $1 "*) return 0 ;; *) return 1 ;; esac; }
select_module() { is_selected "$1" || selected="$selected$1 "; }

# owner prints the directory of the nearest module containing the file, or
# nothing when no module does.
owner() {
  local file="$1" dir best="" best_len=-1
  for dir in "${dirs[@]}"; do
    if [ "$dir" = "$ROOT_MODULE" ]; then
      [ "$best_len" -lt 0 ] && best="$dir" && best_len=0
      continue
    fi
    case "$file" in
      "$dir"/*) [ "${#dir}" -gt "$best_len" ] && best="$dir" && best_len="${#dir}" ;;
    esac
  done
  echo "$best"
}

for file in "$@"; do
  case "$WORKSPACE_FILES" in
    *" $file "*)
      echo "$LOG_PREFIX $file changed; testing every module"
      for dir in "${dirs[@]}"; do select_module "$dir"; done
      continue
      ;;
  esac
  dir="$(owner "$file")"
  [ -n "$dir" ] && select_module "$dir"
done

# Add dependents until nothing more is added.
grew=1
while [ "$grew" -eq 1 ]; do
  grew=0
  for i in "${!dirs[@]}"; do
    is_selected "${dirs[$i]}" && continue
    for j in "${!dirs[@]}"; do
      is_selected "${dirs[$j]}" || continue
      if grep -qxF "${paths[$j]}" <<<"${requires[$i]}"; then
        echo "$LOG_PREFIX ${dirs[$i]} depends on ${dirs[$j]}; testing it too"
        select_module "${dirs[$i]}"
        grew=1
        break
      fi
    done
  done
done

if [ "$selected" = " " ]; then
  echo "$LOG_PREFIX no Go module changed; skipping the Go tests"
  exit 0
fi

for dir in "${dirs[@]}"; do
  if ! is_selected "$dir"; then
    echo "$LOG_PREFIX skipping $dir: unchanged"
    continue
  fi
  # A module that only pins tool versions has no package for its build tags,
  # and `go test ./...` fails on it with "no packages to test". Skipped only
  # when go list succeeds with nothing: a module go list cannot load is tested
  # anyway, so its error fails the push instead of hiding behind a skip.
  if packages="$(cd "$dir" && "$GO_CMD" list -e ./... 2>/dev/null)" && [ -z "$packages" ]; then
    echo "$LOG_PREFIX skipping $dir: no packages to test"
    continue
  fi
  echo "$LOG_PREFIX testing $dir"
  (cd "$dir" && "$GO_CMD" test "${test_flags[@]}" ./...)
done
