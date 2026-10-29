# Security policy

## Reporting a vulnerability

Report privately through GitHub's **[Report a
vulnerability](https://github.com/oleg-tkachuk/limes/security/advisories/new)**
form. Do not open a public issue for anything exploitable.

Include the version or commit, the authority the token started with and the
authority you ended with, and the shortest reproduction.

Expect an acknowledgement within 3 working days and an assessment within 10.

## What CI vouches for

A pull request runs the workflows in its own head, so a change to
`.github/` can make **All checks passed** pass without the checks behind it.
A fork's pull request is never merged automatically, and neither is one that
changes what CI runs — `.github/`, the Taskfile, `.golangci.yml`, `tools/`,
the root `go.mod`, `go.work`, vendored code or the Renovate config (the full
list is in CONTRIBUTING.md): a person reviews those files and merges it. Auto-merge decides this
from `main`'s own workflow, which a pull request cannot change.

## Supported versions

Pre-1.0: only the latest release of each module — `limes`, `pgstore`,
`redisreplay`, `otelmetric`, `exchange` and `biscuit`. There are no maintenance branches.
