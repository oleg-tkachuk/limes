# ADR-0016: Every module released from one release commit

**Status:** Accepted
**Date:** 2026-10-31
**Deciders:** Oleg Tkachuk

## Context

A nested module requires a released limes, and a version that is not
tagged cannot be required. So a release of every module is a chain of runs
and pull requests, each waiting for the one before:

1. A release run tags limes.
2. `require-limes.ts` opens a pull request raising each nested module's
   requirement to it; CI runs, `auto-merge.yaml` merges it.
3. CI runs on main, and a release run tags the nested modules.
4. `changelog.ts propose` opens a pull request with the new entries; CI
   runs, it merges, CI runs on main again.
5. A candidate of 1.0 adds a third: `candidates.ts propose`, its row in
   the migration guide.

That is five CI cycles and two or three bot pull requests for one release,
about forty minutes, and main is red in between: the "Released limes" jobs
fail from a limes release until the requirement is raised, which
`requiredConclusion`, `GATES` and `auto-merge.yaml` each work around. A pull
request cut while a proposal is open fails the changelog or candidates
check until the proposal merges.

The chain exists only because limes and its nested modules are tagged at
different commits. Go does not require that: a module may require a
version tagged on the same commit as itself, since the proxy resolves both
from the tags. OpenTelemetry-Go releases its modules this way (`multimod`).

Main's ruleset takes a change only through a pull request, and only the
release's deploy key creates a release tag (`repo-settings.ts`). Both stay.

## Decision

1. **One release pull request carries every module's release.** A plan
   step (`release-plan.ts`) runs where a release run starts now: after a
   green push, for the modules the policy finds due
   (`release-policy.json`), or by hand for those named. For each module it
   runs semantic-release as a dry run, for the next version and its notes
   alone. When limes is released, every nested module that requires it is
   raised to limes's next version and released at least as a patch. No
   commit carries that raise any more, so the plan applies the rule itself
   and writes the module's `### Dependencies` line, as the `build` commit
   of the requirement does now. One commit on
   `release/next` then holds, for every module released:
   - its `require` of limes, raised, and the followers (`examples`) tidied;
   - its changelog's new entry, from the notes of the dry run.

   Its pull request, `chore(release): …` naming the tags, is opened or
   rebuilt from main on every run, and `auto-merge.yaml` merges it once
   green, but one that cuts a major of a module: that waits for the
   maintainer's merge. A `chore` commit releases nothing itself.

2. **The merge commit is tagged, every module at once.** After CI passes
   on main, a tag step (`release-tag.ts`) runs first, then the plan, in the
   same run: the plan counts commits from the tags the step has just cut.
   The step finds the release pull request's merge commit and tags, with the deploy key, each module whose changelog
   opens with an entry no tag names yet: the changelog is the manifest of
   the release, so nothing is computed twice. Each tag is published as a
   GitHub release with that entry as its notes, then GitHub's list of merged
   pull requests, as now, and the Go proxy is told of it.

3. **Nothing waits on a released limes.** A nested module's requirement
   names the limes tagged on the same commit, and CI tests it against that
   commit through the `replace` in its `go.mod`, so the "Released limes"
   jobs, `GATES` and their workarounds go. CI instead holds every nested
   module's requirement to a version of limes not above the one the
   changelog last names.

4. **A candidate needs no commit.** A prerelease tags main's head as now.
   Its GitHub release lists each nested module's release at that commit,
   which the migration guide's table holds now; the guide links to the
   prereleases instead, and `candidates.ts` goes.

5. **The changelogs are checked strictly.** Every entry is written before
   its tag, so `changelog.ts check` holds each module's changelog to one
   rule, the one the tag step reads: every stable tag reachable from HEAD
   has an entry, and every entry names such a tag but the newest, which
   may be the release still to be tagged.

## Consequences

- **A release takes two CI cycles,** the release pull request's and
  main's, then the tag step: about fifteen minutes, against forty. No bot
  pull request but the release one, and main is never red waiting for a
  release.
- **Removed:** `require-limes.ts` and its job, `changelog.ts propose` and
  its job, `candidates.ts` and the table, the `released-limes` CI job,
  `GATES` and `requiredConclusion`'s exception for it, and `build` in the
  policy's `immediateTypes`. `release.ts` keeps
  publishing, the Go proxy warm-up and the policy, and loses its own
  `semantic-release` tag push.
- **A commit merged between the plan and the merge** is in the tagged
  code but not in the notes. The plan is rebuilt on every push to main, so
  the window is one CI cycle; the next release lists the commit.
- **A release pull request that fails CI** blocks no other merge. It
  stays open and is rebuilt on the next push, as `require-limes/modules`
  is now.
- **1.0 is cut the same way.** Once #42's breaking commit is on main,
  the next plan computes limes 1.0.0, and its release pull request waits
  for the maintainer's merge.
- **A nested module with nothing of its own is still released** when limes
  is, for its requirement, as now. Fewer modules would mean fewer such
  releases; which modules earn their own `go.mod` is outside this ADR.

## Rollout

Each step is one pull request, tested in `.github/ci` before it is wired:

1. This ADR.
2. `release-plan.ts` and `release-tag.ts`, with tests, not yet called.
3. The plan run in shadow beside the old flow, for one real release: its
   versions and notes are compared with those the old flow cut.
4. `release.yaml` and `ci.yaml` switched to them; `require-limes.ts`,
   `changelog.ts propose`, the `released-limes` job and `GATES` removed.
   With them: the tag step seeds a new nested module's release line, as
   `release.ts` does now; the plan marks a release pull request that cuts
   a major so `auto-merge.yaml` leaves it, by a label, not a `!` the
   breaking-marker guards would read; and `pushAs` moves out of
   `require-limes.ts`.
5. Candidates: the nested modules' releases in the prerelease's notes,
   the table and `candidates.ts` removed.
6. `changelog.ts check` made strict.
