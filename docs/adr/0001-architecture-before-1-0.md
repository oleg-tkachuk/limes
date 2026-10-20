# ADR-0001: Consolidate the architecture behind the frozen layout

**Status:** Proposed
**Date:** 2026-10-20
**Deciders:** Oleg Tkachuk

## Context

limes is about to cut v1.0. From then on every exported identifier, and
every method of every interface a consumer implements, is frozen until a
`/v2` module path ([api-review-v1.md](../api-review-v1.md)). The package
layout was settled there and is not reopened here: `jwks` and `dpop` are leaf
packages the root imports and the meter is package `meter` (decision 7),
`inspect` is a subpackage (decision 5), and `otelmetric.Instrument` is the one
decorator, with a fixed forwarding list (I8).

What the review did not cover is the code behind that surface and the
machinery around it. The architecture as it stands has three layers:

| Layer | Size (non-test lines) | Shape |
|---|---|---|
| Root package | 5,932 in 26 files; 185 exported top-level identifiers | issue, delegate, verify, Biscuit, revocation caches, refusals and HTTP answers, caveat checks, ceilings, store contracts |
| Modules | pgstore 4,479; memstore 1,933 in one file | `meter`, `memstore`, `jwks`, `dpop`, `inspect` in the root module; `pgstore`, `redisreplay`, `otelmetric`, `examples` modules of their own |
| CI and release | `.github/ci` 3,194 TS; `.github/release` 399 CJS; workflows 1,269 YAML | `changes.ts` gates jobs by module group; `release.ts` runs semantic-release per module; `require-limes.ts` raises each nested module's requirement after a limes release |

The forces:

- **The freeze.** A change to an exported name after 1.0 waits for v2.
  A change behind the surface can land in any minor.
- **Facts stated twice.** The same fact lives in several places that must
  agree, and some of those pairs have no test that fails when they drift.
  A mismatch there is silent: a job that never runs, a refusal answered with
  the wrong status.
- **Uneven module layout.** `pgstore` and `examples` build against the limes
  of the same commit through a `replace`; `redisreplay` and `otelmetric` build
  against the released one. Only pgstore's release is gated on a job against
  the released limes (`.github/ci/release.ts:53`).
- **Duplicated logic.** Issue and delegate run the same validation sequence;
  memstore and pgstore carry identical helpers.

## Decision

Keep the layout the API review settled. Refactor behind it in small steps,
each releasable in a minor, and give every fact that is stated twice either a
single source or a test that pins the copies together. Nothing in this ADR
adds, renames or removes an exported identifier, except the items marked
**API** below, which are listed for a decision of their own.

## Options considered

### Option A: consolidate behind the frozen layout

| Dimension | Assessment |
|---|---|
| Complexity | Low: each item is local and lands on its own |
| Cost | A handful of small pull requests per layer |
| Risk to 1.0 | None: no exported identifier changes |
| Reversibility | High |

**Pros:** ships continuously; every step is checked by the existing suites
(`storetest`, `metertest`, `dpoptest`, `jwkstest`, `node --test`).
**Cons:** the root package stays one package of 26 files.

### Option B: split the root package into public subpackages

Move Biscuit, the revocation caches and the HTTP refusals into packages of
their own before 1.0.

| Dimension | Assessment |
|---|---|
| Complexity | High |
| Cost | Breaks most consumers; delays 1.0 |
| Feasibility | Blocked by import cycles |

**Pros:** smaller packages, narrower imports for consumers who verify only.
**Cons:** the split does not hold. `Narrows` needs `Capability` and is called
from the root (`issuer.go:465`); Biscuit is methods on `Issuer` and `Verifier`
(`Issuer.Biscuit`, `Verifier.BiscuitCopy`) over the unexported `grant`. A
subpackage importing the root cannot be imported by it, so each split needs
another `internal/link` indirection (`internal/link/link.go:1-7`). Decision 7
split `jwks` and `dpop` precisely because they do not depend on `Capability`;
these concerns do.

### Option C: defer everything to v2

**Pros:** no work now.
**Cons:** almost none of the items need v2. Unpinned duplicates keep drifting
in the meantime, and the refactors that do not touch the API get no cheaper
by waiting.

## Trade-off analysis

The discriminating force is the freeze. Option B pays its whole cost against
it and is blocked by the dependency on `Capability`. Option C treats every
item as if it touched the API, when almost none does. Option A does what the
freeze allows now and names the rest, so the API items can be decided before
1.0 or deliberately left for v2.

## Consequences

- Each layer gets a short list of independent pull requests, below.
- Every pair of copies that must agree is either merged into one source or
  pinned by a test that fails on drift.
- The root package stays one package; navigating it is done by file, so
  files that mix concerns are split by concern without changing a name.
- To revisit: the **API** items (question 2). Question 1 is decided.

## Action items

### Root package

1. [x] Pin `proofRefusals` (`verifier.go:502`) to the four dpop rows of
   `refusalRules` (`reason.go:69-72`), or derive it from them: today no test
   fails when the two disagree.
2. [ ] Extract the validation sequence `issue` (`issuer.go:181`) and
   `delegate` (`issuer.go:350`) share — subject, audience, caveats,
   thumbprint, expiry, id, not-before, sign, insert — into one function;
   `delegate` is 137 lines, `Verifier.verify` (`verifier.go:271`) 117.
3. [x] Move the generic `ttlCache` (`cache.go:120` to the end) to
   `internal/`; it uses no root type.
4. [ ] Split `types.go` (errors, units, `Op`, `Principal`, `Capability`,
   `grant`, `Caveats`) and `biscuit_revocation.go` (interfaces, `BiscuitCopy`,
   a `Verifier` method, the cache) by concern. File moves only.
5. [ ] **API.** `RevocationCache` and `BiscuitRevocationCache` have the same
   config, `Clear` and `Sweep` shape (`cache.go:41`,
   `biscuit_revocation.go:155`). Merging them changes exported types.

### Modules

6. [x] Add a Redis client prefix to `forbiddenImports`
   (`isolation_test.go:52`): `redisreplay/go.mod` promises limes never
   resolves one, and nothing checks it.
7. [ ] Stop the nested modules' tests importing `internal/link`
   (`pgstore/*_test.go`, `otelmetric/store_test.go`). A change to that package
   breaks their build against the released limes, the pinned pgstore job
   included.
8. [x] Run `otelmetric.Instrument` over memstore through `metertest.Run`: it
   returns a `meter.UsageStore` no suite checks.
9. [ ] Check that `metertest` has a case for every tenant-budget filter:
   memstore runs it in Go (`memstore.go:1729`), pgstore in SQL
   (`pgstore/usage.go:412`), and `pgstore/go.mod` already retracts a release
   where the two disagreed. The small helpers both carry (`max0`,
   `sameCopyIDs`) stay duplicated: sharing them needs an exported name or
   another cross-module `internal` import (item 7).
10. [x] Raise `examples`' pgstore requirement from v0.10.1, which the
    `replace` hides (`examples/go.mod:21`).
11. [x] Fix two stale comments: `go.mod:9` names `UsageStore[TX]`, generic no
    longer; `ci.yaml:320` says Renovate raises pgstore's requirement, which
    `require-limes.ts` does.

### CI and release

12. [ ] Derive the module list from one place. `modules.cjs` and
    `release.yaml`'s choice options are pinned together
    (`release.test.ts`); `changes.ts`'s `Group`, `ci.yaml`'s outputs and
    `if:`s, the floor job's directories, the Taskfile's `*_DIR` variables and
    `require-limes.ts`'s `FOLLOWERS` are not.
13. [ ] One module for the run conclusions and the CI workflow path:
    `RED` (`merge-green.ts:40`, `main-health.ts:25`), `PASSING`
    (`checks.ts:16`, `ci-run.ts:35`), `CI_WORKFLOW` (`merge-green.ts:38`,
    `ci-run.ts:28`).
14. [ ] Pin `auto-merge.yaml`'s `workflows: [CI]` to `ci.yaml`'s
    `name: CI`: a rename leaves merging dead with nothing said.
15. [ ] Read the tag semantic-release cut from its result rather than by
    diffing the tags on HEAD before and after the run
    (`.github/ci/lib/releases.ts`).

## Open questions

1. **Module layout.** Should redisreplay and otelmetric build against the
   limes of the same commit, as pgstore does, with a job against the released
   limes gating their release? **Decided on 2026-10-20: yes.** Every nested
   module carries the `replace`, and ci.yaml's `released-limes` job gates each
   one's release (`GATED` in `.github/ci/release.ts`).
2. **API items.** Should item 5 land before 1.0, or wait for v2?
