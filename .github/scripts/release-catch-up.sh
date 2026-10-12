#!/usr/bin/env bash
# release-catch-up.sh — bring the release checkout up to main, then decide.
#
# Usage: release-catch-up.sh <dispatched-sha>
#
# semantic-release refuses to publish from a branch behind its remote, and
# exits 0 when it does: a commit landing on main between this job's checkout
# and semantic-release would drop the release without a word, and a
# documentation-only commit dispatches none of its own. So, right before
# semantic-release runs, the checkout is fast-forwarded to main's head and
# release-head-check.sh decides afresh against it. Prints `release=…` for
# GITHUB_OUTPUT.

set -euo pipefail

readonly REMOTE=origin
readonly BRANCH=main

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

git fetch --quiet "$REMOTE" "$BRANCH"
if [ "$(git rev-parse HEAD)" != "$(git rev-parse FETCH_HEAD)" ]; then
    git merge --quiet --ff-only FETCH_HEAD
    echo "main moved to $(git rev-parse HEAD) since the checkout" >&2
fi
exec "$here/release-head-check.sh" "$@"
