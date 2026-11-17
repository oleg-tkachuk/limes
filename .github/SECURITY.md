# Security policy

## Reporting a vulnerability

Report privately through GitHub's **[Report a
vulnerability](https://github.com/oleg-tkachuk/limes/security/advisories/new)**
form. Do not open a public issue for anything exploitable.

Include the version or commit, the authority the token started with, the
authority you ended with, and the shortest reproduction.

Expect an acknowledgement within 3 working days and an assessment within 10.

## What CI vouches for

A pull request runs the workflows in its own head, so a change to
`.github/` can make **All checks passed** pass without the checks behind
it. A pull request from a fork, or one that changes what CI runs
([CONTRIBUTING.md](CONTRIBUTING.md#how-a-change-lands) lists the paths), is
never merged automatically: a person reviews it and merges it. Auto-merge
decides this from `main`'s own workflow, which a pull request cannot
change.

## Supported versions

The latest release of the v1 line of each module — `limes`, `pgstore`,
`redisreplay`, `otelmetric`, `exchange` and `biscuit`. There are no
maintenance branches.
