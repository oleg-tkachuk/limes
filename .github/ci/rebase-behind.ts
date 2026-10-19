// Advance the merge queue: rebase onto main the first pull request in line,
// and only that one, when it has fallen behind.
//
// Usage: GH_TOKEN=… REPO=owner/name node rebase-behind.ts
//
// main requires every pull request to be up to date with it before it
// merges. Rebasing every open one after each merge reran CI on all of them,
// and only the first to finish merged, so the rest were rebased and rerun
// again. The queue instead rebases one: the lowest-numbered pull request that
// would merge on its own once green. The rest wait unrebased, and their turn
// comes when the one ahead merges (a push to main) or its CI fails (the run
// completing), the two events auto-merge.yaml runs this on. A first in line
// that is not behind is left alone: its CI is running, or merge-green is
// about to merge it.
//
// In line: same-repository, non-draft pull requests into main whose author
// can push here, with no known conflict (DIRTY), whose required check has not
// failed at their head, and that something merges once green: GitHub's
// auto-merge is enabled, or merge-green would merge it, as it does every pull
// request that changes no CI definition (merge-policy.ts). Renovate's are
// left out: Renovate rebases and merges its own. A pull request that changes
// CI without auto-merge waits for a person, and would hold the queue if it
// were in it. Whether one is behind is read from the comparison of its head
// with main, not from its merge state: GitHub computes that in the
// background, and seconds after a push to main it still reads CLEAN or
// UNKNOWN for a branch the push left behind.
//
// Only one whose author can push to this repository is rebased, so the token
// never rewrites a branch on behalf of someone who could not have pushed it.
// The rebase names the head it compared (expected_head_sha): a branch pushed
// to since is left as its owner left it. One that cannot be compared or
// rebased — a conflict GitHub finds only while rebasing — is reported, and
// the next in line takes its place.
//
// Without GH_TOKEN it warns and does nothing: a push made with the
// workflow's own GITHUB_TOKEN would start no CI on the rebased branch
// (auto-merge.yaml).

import { info, notice, warning } from "./lib/actions.ts";
import { optionalEnv, requireEnv } from "./lib/env.ts";
import { gitHubFor, type CheckRun, type Comparison, type GitHub, type QueuedPullRequest } from "./lib/github.ts";
import { runMain } from "./lib/main.ts";
import { BASE_BRANCH, canPush, changesCi, filesListedInFull, isRenovate } from "./merge-policy.ts";

// PR_LIMIT is the most pull requests one run looks at.
export const PR_LIMIT = 200;
// DIRTY is the merge state of a pull request whose branch conflicts with main.
const DIRTY = "DIRTY";
// REQUIRED_CHECK is the check main requires, and PASSING the conclusions of
// it that keep a pull request in line: any other a finished run of it ends
// with — failed, cancelled, timed out — takes the pull request out until it
// is pushed to or its run is retried, as no merge would follow it. Only the
// latest run of the check counts: a run superseded on the same head — the
// pull request's title edited, say — is cancelled beside the run that
// replaced it. A run still going has no conclusion yet, and stays in line.
export const REQUIRED_CHECK = "All checks passed";
const PASSING: readonly string[] = ["SUCCESS", "NEUTRAL", "SKIPPED"];

// Outcome is what one run of the queue did.
export const Outcome = {
  Rebased: "rebased",
  UpToDate: "up to date",
  NoneInLine: "none in line",
} as const;
export type Outcome = (typeof Outcome)[keyof typeof Outcome];

// latestRequiredCheck is the latest run of REQUIRED_CHECK among checks, or
// undefined when it has not run.
export function latestRequiredCheck(checks: readonly CheckRun[]): CheckRun | undefined {
  return checks
    .filter((c) => c.name === REQUIRED_CHECK)
    .sort((a, b) => (a.startedAt ?? "").localeCompare(b.startedAt ?? ""))
    .at(-1);
}

// mayBeInLine reports whether a pull request is a candidate for the queue,
// on what the listing says of it alone.
export function mayBeInLine(pr: QueuedPullRequest): boolean {
  if (pr.draft || pr.fromFork || isRenovate(pr.author) || pr.mergeState === DIRTY) {
    return false;
  }
  const conclusion = latestRequiredCheck(pr.checks)?.conclusion;
  return conclusion === undefined || PASSING.includes(conclusion);
}

// mergesItself reports whether a pull request whose head compares with
// BASE_BRANCH as comparison, changing changedFiles files, is one merge-green
// merges without GitHub's auto-merge: every file its head changes is listed,
// and none is a CI definition. The files are the head's the rebase names, as
// merge-green reads those of the commit it merges.
export function mergesItself(comparison: Comparison, changedFiles: number | undefined): boolean {
  return filesListedInFull(comparison.files.length, changedFiles) && changesCi(comparison.files) === undefined;
}

// advanceQueue rebases the first pull request in line if it is behind main.
export async function advanceQueue(github: GitHub): Promise<Outcome> {
  // Lowest number first: the order pull requests joined the queue in.
  const queue = (await github.openPullRequests(BASE_BRANCH, PR_LIMIT)).filter(mayBeInLine).sort((a, b) => a.number - b.number);
  for (const pr of queue) {
    if (!(await canPush(github, pr.author.login))) {
      notice(`#${pr.number} is by ${pr.author.login}, who cannot push here; it is not in line`);
      continue;
    }
    let comparison: Comparison;
    try {
      comparison = await github.compare(BASE_BRANCH, pr.headSha);
    } catch {
      warning(`could not compare #${pr.number} with ${BASE_BRANCH}; the next in line goes first`);
      continue;
    }
    if (!pr.autoMerge && !mergesItself(comparison, pr.changedFiles)) {
      notice(`#${pr.number} changes what CI runs, or more files than were listed, and has no auto-merge; it waits for a person, not in line`);
      continue;
    }
    if (comparison.behindBy === 0) {
      info(`rebase-behind: #${pr.number} is first in line and up to date`);
      return Outcome.UpToDate;
    }
    try {
      await github.rebaseBranch(pr.number, pr.headSha);
      info(`rebase-behind: rebased #${pr.number}, first in line`);
      return Outcome.Rebased;
    } catch {
      warning(`could not rebase #${pr.number}; it needs a manual update, and the next in line goes first`);
    }
  }
  info("rebase-behind: no pull request in line");
  return Outcome.NoneInLine;
}

async function main(): Promise<void> {
  const token = optionalEnv("GH_TOKEN");
  if (token === undefined) {
    warning("AUTOMERGE_TOKEN is not set; the merge queue does not move (see auto-merge.yaml)");
    return;
  }
  await advanceQueue(gitHubFor(token, requireEnv("REPO")));
}

if (import.meta.main) {
  await runMain(main);
}
