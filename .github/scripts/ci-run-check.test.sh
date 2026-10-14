#!/usr/bin/env bash
# ci-run-check.test.sh — ci-run-check.sh releases only for a passed CI run of
# ci.yaml on a push to main for the dispatched sha, and its list of required
# jobs is the one ci.yaml's required check stands for, which dispatch-release
# waits for.
#
# A fake gh stands in for the API: it answers the run from FAKE_RUN and the
# jobs, as the real gh's --jq would print them, from FAKE_JOBS.
#
# Runs from `task verify-all` — no Docker, no network.

set -euo pipefail

root="$(git rev-parse --show-toplevel)"
script="$root/.github/scripts/ci-run-check.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

cat >"$work/gh" <<'GH'
#!/usr/bin/env bash
case "$*" in
    *"/jobs"*) printf '%s\n' "$FAKE_JOBS" ;;
    *) printf '%s\n' "$FAKE_RUN" ;;
esac
GH
chmod +x "$work/gh"
export PATH="$work:$PATH"

readonly REPO=oleg-tkachuk/limes
readonly SHA=0123456789abcdef0123456789abcdef01234567
readonly OTHER=fedcba9876543210fedcba9876543210fedcba98
readonly RUN_ID=42
export GITHUB_REPOSITORY="$REPO"

run_json() { # path, event, branch, sha, [repository]
    printf '{"path":"%s","event":"%s","head_branch":"%s","head_sha":"%s","repository":{"full_name":"%s"}}' \
        "$1" "$2" "$3" "$4" "${5-$REPO}"
}
jobs_tsv() { # conclusion for Verify
    local name
    while IFS= read -r name; do
        if [ "$name" = Verify ]; then
            printf '%s\tcompleted\t%s\n' "$name" "$1"
        else
            printf '%s\tcompleted\tsuccess\n' "$name"
        fi
    done < <("$script" --print-required)
    printf 'Dispatch release\tin_progress\t\n'
}

failures=0
check() { # name, want (pass|fail), run json, jobs, [run id], [sha]
    local got=pass
    FAKE_RUN="$3" FAKE_JOBS="$4" "$script" "${5-$RUN_ID}" "${6-$SHA}" >/dev/null 2>&1 || got=fail
    if [ "$2" != "$got" ]; then
        printf 'FAIL %s: want %s, got %s\n' "$1" "$2" "$got"
        failures=$((failures + 1))
    fi
}

good="$(run_json .github/workflows/ci.yaml push main "$SHA")"
check "a passed run" pass "$good" "$(jobs_tsv success)"
check "a skipped job" pass "$good" "$(jobs_tsv skipped)"
check "a failed job" fail "$good" "$(jobs_tsv failure)"
check "a running job" fail "$good" "$(jobs_tsv success | sed 's/^Verify\tcompleted\tsuccess/Verify\tin_progress\t/')"
check "a missing job" fail "$good" "$(jobs_tsv success | grep -v '^Verify')"
check "another sha" fail "$(run_json .github/workflows/ci.yaml push main "$OTHER")" "$(jobs_tsv success)"
check "another workflow" fail "$(run_json .github/workflows/codeql.yaml push main "$SHA")" "$(jobs_tsv success)"
check "a pull request" fail "$(run_json .github/workflows/ci.yaml pull_request main "$SHA")" "$(jobs_tsv success)"
check "another branch" fail "$(run_json .github/workflows/ci.yaml push feature "$SHA")" "$(jobs_tsv success)"
check "another repository" fail "$(run_json .github/workflows/ci.yaml push main "$SHA" someone/fork)" "$(jobs_tsv success)"
check "no run id" fail "$good" "$(jobs_tsv success)" "" "$SHA"
check "no sha" fail "$good" "$(jobs_tsv success)" "$RUN_ID" ""

# The required jobs are the names of the jobs the required check needs, and
# dispatch-release waits for that check.
yq() { go tool -modfile="$root/tools/yq/go.mod" yq "$@" "$root/.github/workflows/ci.yaml"; }
want="$("$script" --print-required | sort)"
got="$(yq '.jobs.checks.needs[] as $id | .jobs[$id].name' | sort)"
if [ "$want" != "$got" ]; then
    printf 'FAIL REQUIRED_JOBS differs from checks.needs:\n%s\n' "$(diff <(echo "$want") <(echo "$got") || true)"
    failures=$((failures + 1))
fi
if ! yq '.jobs.dispatch-release.needs[]' | grep -qx checks; then
    echo "FAIL dispatch-release does not wait for the required check"
    failures=$((failures + 1))
fi

if [ "$failures" -gt 0 ]; then
    exit 1
fi
echo "ci-run-check: every case matches"
