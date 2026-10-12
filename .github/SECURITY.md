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
A fork's pull request is never merged automatically, and one that touches
`.github/` is merged only after those files are reviewed by hand.

## Supported versions

Pre-1.0: only the latest release. There are no maintenance branches.
