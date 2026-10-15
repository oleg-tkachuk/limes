#!/usr/bin/env bash
# without-workflow-commands.sh — run a command whose output Actions reads as
# text only.
#
# Usage: without-workflow-commands.sh <command> [args...]
#
# GitHub Actions acts on a line of a step's output that reads as a workflow
# command (`::add-mask::`, `::error::`, …). semantic-release logs every commit
# message it analyses, and anyone who can land a commit chooses that text,
# in a step that holds a token with write access. Between `::stop-commands::`
# and the same random token echoed back, Actions runs no workflow command
# (docs: "Stopping and starting workflow commands"); the token is fresh each
# run, so no commit can name it. The command's exit status is this script's.

set -euo pipefail

# TOKEN_BYTES is how much randomness the resume token carries.
readonly TOKEN_BYTES=16

if [ "$#" -eq 0 ]; then
    echo "usage: $0 <command> [args...]" >&2
    exit 2
fi

token="$(od -An -N"$TOKEN_BYTES" -tx1 /dev/urandom | tr -d ' \n')"
echo "::stop-commands::$token"
status=0
"$@" || status=$?
echo "::$token::"
exit "$status"
