// Propose that the nested modules require the limes release a run cut.
//
// Usage: GH_TOKEN=… GITHUB_REPOSITORY=… node require-limes.ts <limes tag>
//
// A nested module requires a released limes, and is released only once its
// tests against the limes it requires pass (release.ts). So every
// limes release is followed by raising that requirement: for every nested
// module of .github/release/modules.cjs at once, this commits it on BRANCH
// and opens its pull request, or updates the one still open to the newer
// release. auto-merge.yaml merges it once green, and its merge releases each
// module it raised: a build commit scoped to LIMES_REQUIREMENT_SCOPE is a
// patch of every nested module whose directory it changes (modules.cjs).
//
// A module's go.mod is read and written by `go mod edit`, then tidied. A
// module that does not require limes, or already requires this release or a
// later one, is left alone. Each of FOLLOWERS is tidied in the same commit.
// Only a pull request from this repository's branch is updated — a fork's of
// the same name never is — and one module's failure leaves it out, the
// others proposed, and fails the run. What it did with each module, and
// the pull request, goes on the run's summary page.
//
// Without GH_TOKEN it warns and does nothing: a push made with the
// workflow's own GITHUB_TOKEN would start no CI on the branch
// (auto-merge.yaml). The token reaches only the push and the API: go, which
// fetches modules and may run their version-control tools, never sees it.

import { existsSync } from "node:fs";
import { join } from "node:path";

import { error, info, summarize, table, warning } from "./lib/actions.ts";
import { optionalEnv, requireEnv } from "./lib/env.ts";
import { git } from "./lib/git.ts";
import { type GitHub, gitHubFor, type PullRequestText } from "./lib/github.ts";
import { runMain } from "./lib/main.ts";
import { ROOT_MODULE, releasedModules, selectModule } from "./lib/modules.ts";
import { pushAs } from "./lib/push.ts";
import { FOLLOWERS, LIMES_MODULE, compareVersions, goEnv, raise, tidyFollowers } from "./lib/go-modules.ts";

export { FOLLOWERS, LIMES_MODULE, compareVersions, goEnv, pushAs };


// BRANCH is the branch the requirement is proposed on, for every nested
// module at once.
export const BRANCH = "require-limes/modules";
// LIMES_REQUIREMENT_SCOPE scopes the commit and its pull request's title, as
// modules.cjs's of the same name releases it; require-limes.test.ts holds
// the two together. Not "limes": the change is the nested modules', which a
// title scoped to limes would read as limes's own.
export const LIMES_REQUIREMENT_SCOPE = "nested";
// BASE_BRANCH is the branch the pull request merges into.
export const BASE_BRANCH = "main";
const REMOTE = "origin";
const RELEASE_TAG = /^v[0-9]+\.[0-9]+\.[0-9]+$/;

// Proposal is what a run did with the pull request.
export const Proposal = { Opened: "opened", Updated: "updated", Unchanged: "unchanged" } as const;
export type Proposal = (typeof Proposal)[keyof typeof Proposal];

// Outcome is what a run did: with the pull request, which it names once
// opened or updated, and the modules it raised, each from the limes it
// required, and those that failed.
export interface Outcome {
  proposal: Proposal;
  pullRequest?: number;
  raised: { dir: string; from: string }[];
  failed: string[];
}

// Context is where a proposal runs and how it pushes.
export interface Context {
  github: GitHub;
  root: string;
  // push pushes branch to the remote, as the token's owner.
  push(branch: string): Promise<void>;
}

// proposeRequirements raises every module of dirs to tag, in one commit on
// BRANCH cut from main as the remote has it now, and opens or updates its
// pull request.
export async function proposeRequirements(ctx: Context, dirs: readonly string[], tag: string): Promise<Outcome> {
  checkProposal(ctx.root, dirs, tag);
  // Fetched afresh: a pull request raising the same requirement may have
  // merged since, and a branch cut from before it would conflict with it.
  await git(["fetch", "--quiet", REMOTE, BASE_BRANCH], ctx.root);
  await git(["switch", "--quiet", "--discard-changes", "-C", BRANCH, `${REMOTE}/${BASE_BRANCH}`], ctx.root);
  const { outcome, changed } = await raiseEach(ctx.root, dirs, tag);
  if (outcome.raised.length === 0) {
    return outcome;
  }
  changed.push(...(await tidyFollowers(ctx.root)));
  const title = requirementTitle(tag, outcome.raised.map((r) => r.dir));
  await git(["add", "--", ...changed.filter((p) => existsSync(join(ctx.root, p)))], ctx.root);
  await git(["commit", "--quiet", "-m", title], ctx.root);
  await ctx.push(BRANCH);
  const modules = outcome.raised.map((r) => `- ${r.dir}: ${r.from} → ${tag}`).join("\n");
  const text = {
    title,
    body: `limes ${tag} is released. This raises the nested modules' requirement of it, so each is tested against, and released with, the limes it now requires:\n\n${modules}`,
  };
  return { ...outcome, ...(await openOrUpdate(ctx.github, text)) };
}

// checkProposal refuses a tag that is no release's, or a directory that is
// no module under root.
function checkProposal(root: string, dirs: readonly string[], tag: string): void {
  if (!RELEASE_TAG.test(tag)) {
    throw new Error(`limes tag ${JSON.stringify(tag)} is not a release's vX.Y.Z`);
  }
  for (const dir of dirs) {
    if (dir === "" || !existsSync(join(root, dir, "go.mod"))) {
      throw new Error(`${JSON.stringify(dir)} is no module`);
    }
  }
}

// raiseEach raises each module's requirement of limes to tag, one that
// fails left as main has it while the others go ahead without it; changed
// are the files raised.
async function raiseEach(root: string, dirs: readonly string[], tag: string): Promise<{ outcome: Outcome; changed: string[] }> {
  const outcome: Outcome = { proposal: Proposal.Unchanged, raised: [], failed: [] };
  const changed: string[] = [];
  for (const dir of dirs) {
    try {
      const from = await raise(root, dir, tag);
      if (from !== undefined) {
        outcome.raised.push({ dir, from });
        changed.push(join(dir, "go.mod"), join(dir, "go.sum"));
      }
    } catch (err) {
      error(`${dir}: ${err instanceof Error ? err.message : String(err)}`);
      outcome.failed.push(dir);
      await git(["checkout", "--quiet", "HEAD", "--", dir], root);
    }
  }
  return { outcome, changed };
}

// openOrUpdate proposes text on BRANCH: the open pull request from it,
// updated, or a new one.
async function openOrUpdate(github: GitHub, text: PullRequestText): Promise<Pick<Outcome, "proposal" | "pullRequest">> {
  const open = await github.sameRepoPullRequest(BRANCH, BASE_BRANCH);
  if (open !== undefined) {
    await github.editPullRequest(open, text);
    info(`updated #${open}: ${text.title}`);
    return { proposal: Proposal.Updated, pullRequest: open };
  }
  const number = await github.createPullRequest(BRANCH, BASE_BRANCH, text);
  info(`opened #${number}: ${text.title}`);
  return { proposal: Proposal.Opened, pullRequest: number };
}

// requirementTitle is the subject of the commit, and the title of the pull
// request, raising dirs' requirement to the limes release tag: the modules
// named, so the title says whose go.mod changes.
export function requirementTitle(tag: string, dirs: readonly string[]): string {
  const named = dirs.length > 1 ? `${dirs.slice(0, -1).join(", ")} and ${dirs.at(-1)}` : (dirs[0] ?? "");
  return `build(${LIMES_REQUIREMENT_SCOPE}): require limes ${tag} in ${named}`;
}

// SUMMARY_HEADER heads the summary table; LEFT_ALONE and FAILED fill the
// cell of a module not raised.
const SUMMARY_HEADER = ["Module", "Requirement of limes"];
const LEFT_ALONE = "left alone: requires this release or a later one, or no limes";
const FAILED = "failed: see the log";

// requirementsSummary is the summary page section of what a run did for
// tag over dirs.
export function requirementsSummary(tag: string, dirs: readonly string[], outcome: Outcome): string {
  const raised = new Map(outcome.raised.map((r) => [r.dir, r.from]));
  const rows = dirs.map((dir) => {
    const from = raised.get(dir);
    if (from !== undefined) {
      return [dir, `${from} → ${tag}`];
    }
    return [dir, outcome.failed.includes(dir) ? FAILED : LEFT_ALONE];
  });
  const pr = outcome.pullRequest === undefined ? "No pull request: nothing to raise." : `Pull request #${outcome.pullRequest} ${outcome.proposal}.`;
  return `### limes ${tag} required by the nested modules\n\n${pr}\n\n${table(SUMMARY_HEADER, rows)}`;
}

// nestedModuleDirs is the directory of every nested module modules.cjs
// releases.
export function nestedModuleDirs(root: string): string[] {
  return releasedModules(root)
    .filter((m) => m !== ROOT_MODULE)
    .map((name) => selectModule(root, name).dir);
}

async function main(): Promise<boolean> {
  const [tag = ""] = process.argv.slice(2);
  const token = optionalEnv("GH_TOKEN");
  if (token === undefined) {
    warning(`AUTOMERGE_TOKEN is not set; the nested modules' requirement of limes ${tag} is not proposed (see release.yaml)`);
    return true;
  }
  const repository = requireEnv("GITHUB_REPOSITORY");
  const root = (await git(["rev-parse", "--show-toplevel"])).trim();
  const ctx: Context = { github: gitHubFor(token, repository), root, push: pushAs(token, repository, root) };
  const dirs = nestedModuleDirs(root);
  const outcome = await proposeRequirements(ctx, dirs, tag);
  summarize(requirementsSummary(tag, dirs, outcome));
  return outcome.failed.length === 0;
}

if (import.meta.main) {
  await runMain(main);
}
