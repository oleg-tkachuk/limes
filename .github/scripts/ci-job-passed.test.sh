#!/usr/bin/env bash
# ci-job-passed.test.sh — ci-job-passed.sh answers passed=true only for a job
# that passed or was skipped, passed=false for one that failed, and fails for
# one missing or unfinished; and the job release.yaml asks about is one ci.yaml
# runs.
#
# A fake gh stands in for the API: it answers the jobs, as the real gh's --jq
# would print them, from FAKE_JOBS.
#
# Runs from `task verify-all` — no Docker, no network.

set -euo pipefail

root="$(git rev-parse --show-toplevel)"
script="$root/.github/scripts/ci-job-passed.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

cat >"$work/gh" <<'GH'
#!/usr/bin/env bash
printf '%s\n' "$FAKE_JOBS"
GH
chmod +x "$work/gh"
export PATH="$work:$PATH"
export GITHUB_REPOSITORY=oleg-tkachuk/limes

readonly JOB="PostgreSQL store, released limes"
readonly RUN_ID=42

jobs_tsv() { # status, conclusion of JOB
    printf 'Verify\tcompleted\tsuccess\n'
    printf '%s\t%s\t%s\n' "$JOB" "$1" "$2"
}

failures=0
check() { # name, want (passed=true|passed=false|error), jobs, [run id], [job]
    local got
    if got="$(FAKE_JOBS="$3" "$script" "${4-$RUN_ID}" "${5-$JOB}" 2>/dev/null)"; then
        :
    else
        got=error
    fi
    if [ "$2" != "$got" ]; then
        printf 'FAIL %s: want %s, got %s\n' "$1" "$2" "$got"
        failures=$((failures + 1))
    fi
}

check "a passed job" passed=true "$(jobs_tsv completed success)"
check "a skipped job" passed=true "$(jobs_tsv completed skipped)"
check "a failed job" passed=false "$(jobs_tsv completed failure)"
check "a cancelled job" passed=false "$(jobs_tsv completed cancelled)"
check "a running job" error "$(jobs_tsv in_progress "")"
check "a missing job" error "$(printf 'Verify\tcompleted\tsuccess\n')"
check "no run id" error "$(jobs_tsv completed success)" ""
check "no job name" error "$(jobs_tsv completed success)" "$RUN_ID" ""

# release.yaml names a job ci.yaml runs, and dispatch-release waits for it.
yq() { go tool -modfile="$root/tools/yq/go.mod" yq "$@"; }
asked="$(grep -o 'ci-job-passed.sh "$CI_RUN" "[^"]*"' "$root/.github/workflows/release.yaml" | sed 's/.*"\$CI_RUN" "\(.*\)"/\1/')"
waited="$(yq '.jobs.dispatch-release.needs[] as $id | .jobs[$id].name' "$root/.github/workflows/ci.yaml")"
if [ -z "$asked" ] || ! grep -qxF "$asked" <<<"$waited"; then
    printf 'FAIL release.yaml asks about %s, which dispatch-release does not wait for\n' "${asked:-no job}"
    failures=$((failures + 1))
fi

if [ "$failures" -gt 0 ]; then
    exit 1
fi
echo "ci-job-passed: every case matches"
