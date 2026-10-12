# shellcheck shell=bash
# gh-retry.sh — gh_retry runs gh, again after a transient failure.
#
# Usage: source "$(dirname "${BASH_SOURCE[0]}")/gh-retry.sh"; gh_retry api …
#
# A single 5xx or dropped connection from the GitHub API would otherwise fail
# the step outright: a release run lost to one 502, a pull request left behind
# main. Only failures that say the server or the network gave out are tried
# again; anything else — a 404, a 422 conflict, a bad argument — is the
# answer, and is returned at once. On success gh's output is printed as it
# was; on failure its last error is.

# How many times gh runs before its failure stands.
readonly GH_RETRY_ATTEMPTS=4
# The wait before the first retry, in seconds; each next one waits that much
# longer. A test sets it to 0.
readonly GH_RETRY_DELAY_SECONDS="${GH_RETRY_DELAY_SECONDS:-5}"
# What gh says on stderr when the failure is transient.
readonly GH_TRANSIENT='HTTP 5[0-9][0-9]|Server Error|connection reset|i/o timeout|TLS handshake timeout|unexpected EOF'

gh_retry() {
    local attempt out err
    err="$(mktemp)"
    for ((attempt = 1; ; attempt++)); do
        if out="$(gh "$@" 2>"$err")"; then
            rm -f "$err"
            printf '%s\n' "$out"
            return 0
        fi
        if ((attempt >= GH_RETRY_ATTEMPTS)) || ! grep -Eq "$GH_TRANSIENT" "$err"; then
            cat "$err" >&2
            rm -f "$err"
            return 1
        fi
        echo "::warning::gh $1 failed transiently (attempt ${attempt} of ${GH_RETRY_ATTEMPTS}): $(head -n 1 "$err")" >&2
        sleep $((GH_RETRY_DELAY_SECONDS * attempt))
    done
}
