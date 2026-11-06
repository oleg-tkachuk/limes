// Hold the release pull request while main has moved past its plan
// (docs/adr/0016-one-release-commit.md, "Stale plans").
//
// Usage: GH_TOKEN=… GITHUB_REPOSITORY=… node release-stale.ts
//
// release-stale.yaml runs this on every push to main, on the commit pushed.
// The plan of the open release pull request was made from its base, the
// parent of the one commit on RELEASE_BRANCH; a commit since then that
// releases a module of the plan, or makes one it leaves out due
// (lib/stale-plan.ts), means its versions are not those of the code its
// merge would tag. The pull request is then made a draft, which
// merge-green.ts leaves to a person and GitHub refuses to merge, with a
// comment naming those commits. release-plan.ts makes the plan again once
// main's CI passes, and makes the pull request ready unless it cuts a major.
// A pull request a draft already gets no second comment.
//
// It does nothing, and says why, when no release pull request is open, when
// its head cannot be fetched into this clone, or when its branch is not one release
// commit cut from main's history: there is no plan base to judge from.
//
// It acts through GH_TOKEN, AUTOMERGE_TOKEN in release-stale.yaml, as
// release-plan.ts does. Without it, it warns and does nothing.

import { info, notice, summarize, warning } from "./lib/actions.ts";
import { optionalEnv, requireEnv } from "./lib/env.ts";
import { git, hasCommit } from "./lib/git.ts";
import { gitHubFor, type GitHub } from "./lib/github.ts";
import { runMain } from "./lib/main.ts";
import { releaseCommits, type ReleaseCommits } from "./lib/modules.ts";
import { loadPolicy, type Policy } from "./lib/release-policy.ts";
import { RELEASE_BRANCH, planBaseOf, short, staleLines, staleModules, type Staled } from "./lib/stale-plan.ts";
import { BASE_BRANCH } from "./merge-policy.ts";

// SUMMARY_HEADING heads what a run says on its summary page.
const SUMMARY_HEADING = "### Release plan";

// Checker is what judging the release pull request needs, GitHub injected
// so a test can stand in for it.
export interface Checker {
  github: GitHub;
  root: string;
  // head is main's commit the plan is judged against.
  head: string;
  commits: ReleaseCommits;
  policy: Policy;
  // fetch brings a commit this clone does not hold from the remote.
  fetch(sha: string): Promise<void>;
}

// REMOTE is where a release pull request's head is fetched from.
const REMOTE = "origin";

// Verdict is what a check found and did: the release pull request, if one
// is open, whether this run made it a draft, and why.
export interface Verdict {
  pullRequest: number | undefined;
  drafted: boolean;
  staled: Staled[];
  reason: string;
}

// staleComment is the comment that says why the release pull request was
// made a draft.
export function staleComment(head: string, base: string, staled: readonly Staled[]): string {
  return [
    `Made a draft: main, at ${short(head)}, is past this plan's base, ${short(base)}, by commits it did not count, so its versions are stale:`,
    "",
    ...staleLines(staled),
    "",
    "The plan is made again once main's CI passes, and this pull request is made ready then unless it cuts a major. Merging it before would tag the versions above on code they do not describe.",
  ].join("\n");
}

// checkReleasePlan makes the open release pull request a draft when the
// commits on main past its plan's base stale it.
export async function checkReleasePlan(c: Checker): Promise<Verdict> {
  const number = await c.github.sameRepoPullRequest(RELEASE_BRANCH, BASE_BRANCH);
  if (number === undefined) {
    return { pullRequest: undefined, drafted: false, staled: [], reason: `no release pull request from ${RELEASE_BRANCH} is open` };
  }
  const pr = await c.github.pullRequest(number);
  if (!(await hasCommit(pr.headSha, c.root))) {
    info(`#${number}'s head ${short(pr.headSha)} is not in this clone; fetching it`);
    await c.fetch(pr.headSha).catch((err: unknown) => info(`fetching ${short(pr.headSha)} failed: ${String(err)}`));
  }
  if (!(await hasCommit(pr.headSha, c.root))) {
    return { pullRequest: number, drafted: false, staled: [], reason: `#${number}'s head ${short(pr.headSha)} is not in this clone; its plan cannot be judged` };
  }
  const based = await planBaseOf(pr.headSha, c.head, c.root);
  if (based.base === undefined) {
    return { pullRequest: number, drafted: false, staled: [], reason: `#${number}: ${based.unknown}; its plan cannot be judged` };
  }
  const staled = await staleModules({ root: c.root, release: pr.headSha, base: based.base, head: c.head, commits: c.commits, policy: c.policy });
  if (staled.length === 0) {
    return { pullRequest: number, drafted: false, staled, reason: `#${number}'s plan, from ${short(based.base)}, is current at ${short(c.head)}` };
  }
  if (pr.draft) {
    return { pullRequest: number, drafted: false, staled, reason: `#${number}'s plan, from ${short(based.base)}, is stale at ${short(c.head)}; it is a draft already` };
  }
  await c.github.setDraft(number, true);
  await c.github.commentOnIssue(number, staleComment(c.head, based.base, staled));
  return { pullRequest: number, drafted: true, staled, reason: `#${number}'s plan, from ${short(based.base)}, is stale at ${short(c.head)}: made a draft` };
}

async function main(): Promise<void> {
  const token = optionalEnv("GH_TOKEN");
  if (token === undefined) {
    warning("AUTOMERGE_TOKEN is not set; the release pull request is not checked (see release-stale.yaml)");
    return;
  }
  const repository = requireEnv("GITHUB_REPOSITORY");
  const root = (await git(["rev-parse", "--show-toplevel"])).trim();
  const head = (await git(["rev-parse", "HEAD"], root)).trim();
  const verdict = await checkReleasePlan({
    github: gitHubFor(token, repository),
    root,
    head,
    commits: releaseCommits(),
    policy: loadPolicy(root),
    fetch: async (sha) => void (await git(["fetch", "--quiet", REMOTE, sha], root)),
  });
  if (verdict.drafted) {
    notice(verdict.reason);
  } else {
    info(verdict.reason);
  }
  const lines = staleLines(verdict.staled);
  for (const line of lines) {
    info(line);
  }
  summarize(`${SUMMARY_HEADING}\n\n${verdict.reason}.${lines.length === 0 ? "" : `\n\n${lines.join("\n")}`}`);
}

if (import.meta.main) {
  await runMain(main);
}
