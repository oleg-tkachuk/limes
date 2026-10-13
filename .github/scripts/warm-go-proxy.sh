#!/usr/bin/env bash
# warm-go-proxy.sh — tell the Go module proxy about a release tag just pushed.
#
# Usage: warm-go-proxy.sh <tag>        e.g. v0.3.0, redisreplay/v0.3.0
#
# proxy.golang.org learns of a version when someone asks for it, and until
# then `go get …@latest` may resolve to the previous one. Asking for the version's .info, the endpoint of
# the GOPROXY protocol `go list -m module@version` would ask, makes it fetch
# the tag now. A tag vX.Y.Z is of the module at the root, this repository's
# path; a tag <dir>/vX.Y.Z of the module in <dir>, whose path is the
# repository's with /<dir> after it.

set -euo pipefail

tag="${1:?usage: warm-go-proxy.sh <tag>}"
repo="${GITHUB_REPOSITORY:-oleg-tkachuk/limes}"
proxy="${GOPROXY_URL:-https://proxy.golang.org}"
# The proxy fetches from GitHub on the first request; give it a few tries.
readonly ATTEMPTS=5
readonly PAUSE_SECONDS=10

# An optional module directory — lower-case path segments — then the version.
readonly RELEASE_TAG='^(([a-z0-9][a-z0-9._-]*/)*)(v[0-9]+\.[0-9]+\.[0-9]+)$'
if [[ ! "$tag" =~ $RELEASE_TAG ]]; then
    echo "!!! $tag is not a release tag ([<dir>/]vX.Y.Z)" >&2
    exit 1
fi
dir="${BASH_REMATCH[1]%/}"
version="${BASH_REMATCH[3]}"
module="github.com/${repo}${dir:+/$dir}"
# The protocol escapes upper-case letters in a module path; this repository's
# path is lower-case, which the check below keeps true.
if [[ "$module" =~ [A-Z] ]]; then
    echo "!!! $module has upper-case letters, which the proxy protocol escapes" >&2
    exit 1
fi

curl -fsS --retry "$ATTEMPTS" --retry-delay "$PAUSE_SECONDS" --retry-all-errors \
    "${proxy}/${module}/@v/${version}.info"
echo
