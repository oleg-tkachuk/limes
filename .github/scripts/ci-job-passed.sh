#!/usr/bin/env bash
# ci-job-passed.sh — did one job of a CI run pass?
#
# Usage: ci-job-passed.sh <ci-run-id> <job-name>
#
# release.yaml asks it of pgstore's tests against the released limes, which
# decide whether pgstore is released, not whether the rest is: a change to
# limes that pgstore must follow lands with pgstore in one pull request, and
# pgstore waits until its go.mod requires a limes that has the change. The
# run itself is vouched for by ci-run-check.sh first; this reads one job of
# it from the API, never from the dispatch's payload.
#
# Prints passed=true for a job that passed or was skipped, passed=false for one
# that failed or was cancelled, for $GITHUB_OUTPUT. Exits non-zero, saying why,
# for a job that is missing or not finished: dispatch-release waits for it, so
# either is a fault, not a verdict.
#
# Needs gh.

set -euo pipefail

# shellcheck source=.github/scripts/gh-retry.sh
source "$(dirname "${BASH_SOURCE[0]}")/gh-retry.sh"

readonly COMPLETED=completed

run_id="${1:-}"
job="${2:-}"
repo="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is not set}"
if [[ ! "$run_id" =~ ^[0-9]+$ ]] || [ -z "$job" ]; then
    echo "!!! usage: ci-job-passed.sh <ci-run-id> <job-name> (got '${run_id}' '${job}')" >&2
    exit 1
fi

jobs="$(gh_retry api --paginate "repos/${repo}/actions/runs/${run_id}/jobs" --jq '.jobs[] | [.name, .status, (.conclusion // "")] | @tsv')"
line="$(awk -F'\t' -v n="$job" '$1 == n' <<<"$jobs" | tail -n 1)"
status="$(cut -f2 <<<"$line")"
conclusion="$(cut -f3 <<<"$line")"
if [ "$status" != "$COMPLETED" ]; then
    echo "!!! job '$job' of CI run $run_id is ${status:-missing}" >&2
    exit 1
fi
case "$conclusion" in
    success | skipped) echo "passed=true" ;;
    *)
        echo "job '$job' of CI run $run_id ended as $conclusion" >&2
        echo "passed=false"
        ;;
esac
