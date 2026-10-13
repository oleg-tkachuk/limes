#!/usr/bin/env bash
# ci-run-check.sh — did the CI run that dispatched this release pass?
#
# Usage: ci-run-check.sh <ci-run-id> <sha>
#
# release.yaml runs on any repository_dispatch of type `release`, and anyone
# holding a write token can send one, naming any sha. This asks the API rather
# than the payload: the run must be ci.yaml on a push to main, for exactly
# that sha, and every job a release needs must have passed or been skipped.
# The dispatching job is part of that run, so the run itself has not finished;
# the jobs it waits for have.
#
# Exits non-zero, saying why, on anything else. Needs gh and jq.

set -euo pipefail

# shellcheck source=.github/scripts/gh-retry.sh
source "$(dirname "${BASH_SOURCE[0]}")/gh-retry.sh"

# The ci.yaml jobs dispatch-release needs, by name. ci-run-check.test.sh
# fails when this list and ci.yaml disagree.
readonly REQUIRED_JOBS=(
    "Changed paths"
    "Verify"
    "PostgreSQL store"
    "Test on the oldest supported Go"
    "API compatibility"
    "Breaking markers"
    "Workflow syntax"
    "Workflow audit"
)
readonly CI_WORKFLOW=.github/workflows/ci.yaml
readonly RELEASE_BRANCH=main
readonly PUSH_EVENT=push

if [ "${1:-}" = "--print-required" ]; then
    printf '%s\n' "${REQUIRED_JOBS[@]}"
    exit 0
fi

run_id="${1:-}"
sha="${2:-}"
repo="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is not set}"
if [[ ! "$run_id" =~ ^[0-9]+$ ]] || [[ ! "$sha" =~ ^[0-9a-f]{40}$ ]]; then
    echo "!!! usage: ci-run-check.sh <ci-run-id> <sha> (got '${run_id}' '${sha}')" >&2
    exit 1
fi

run="$(gh_retry api "repos/${repo}/actions/runs/${run_id}")"
field() { jq -r "$1" <<<"$run"; }
problems=()
[ "$(field .path)" = "$CI_WORKFLOW" ] || problems+=("run is $(field .path), not $CI_WORKFLOW")
[ "$(field .event)" = "$PUSH_EVENT" ] || problems+=("run was triggered by $(field .event), not $PUSH_EVENT")
[ "$(field .head_branch)" = "$RELEASE_BRANCH" ] || problems+=("run is on $(field .head_branch), not $RELEASE_BRANCH")
[ "$(field .head_sha)" = "$sha" ] || problems+=("run tested $(field .head_sha), not $sha")
[ "$(field .repository.full_name)" = "$repo" ] || problems+=("run belongs to $(field .repository.full_name)")

jobs="$(gh_retry api --paginate "repos/${repo}/actions/runs/${run_id}/jobs" --jq '.jobs[] | [.name, .status, (.conclusion // "")] | @tsv')"
for name in "${REQUIRED_JOBS[@]}"; do
    line="$(awk -F'\t' -v n="$name" '$1 == n' <<<"$jobs" | tail -n 1)"
    status="$(cut -f2 <<<"$line")"
    conclusion="$(cut -f3 <<<"$line")"
    if [ "$status" != completed ]; then
        problems+=("job '$name' is ${status:-missing}")
    elif [ "$conclusion" != success ] && [ "$conclusion" != skipped ]; then
        problems+=("job '$name' ended as $conclusion")
    fi
done

if [ "${#problems[@]}" -gt 0 ]; then
    printf '!!! CI run %s does not vouch for %s:\n' "$run_id" "$sha" >&2
    printf '    %s\n' "${problems[@]}" >&2
    exit 1
fi
echo "CI run $run_id passed for $sha"
