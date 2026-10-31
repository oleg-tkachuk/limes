// The release pull request: every module due a release, released from one
// commit (docs/adr/0016-one-release-commit.md).
//
// planReleases finds, on the checked-out commit, each module the policy
// finds due — or, by hand, each one asked for — and its next version and
// notes from a semantic-release dry run, which must run on main, so the plan
// is made before any branch is cut. When limes is in the plan, each nested
// module that requires an older limes is raised to it and released with
// it: with its own notes, when it has commits that release, or as a patch
// whose notes are the requirement alone. Each planned release's notes are
// its changelog entry.
//
// proposeRelease writes the plan on BRANCH, cut from the checked-out
// commit — each raised requirement, the followers tidied, each entry first
// in its module's changelog — in one commit, and opens or updates its pull
// request. The commit is a chore, which releases nothing itself; tagging its
// merge is release-tag.ts's. Nothing here calls them yet.

import { existsSync } from "node:fs";
import { join } from "node:path";

import { addEntry, entryFrom, entryVersion } from "./changelog.ts";
import { info } from "./lib/actions.ts";
import { git } from "./lib/git.ts";
import { type GitHub, type PullRequestText } from "./lib/github.ts";
import { FOLLOWERS, compareVersions, raise, requiredLimes, tidyFollowers } from "./lib/go-modules.ts";
import { ROOT_MODULE, releasedModules, selectModule, type Module, type ReleaseCommits } from "./lib/modules.ts";
import { nextVersion, parseVersion } from "./lib/prerelease.ts";
import { Level, isDue, lastStableTag, pendingCommits, type Policy } from "./lib/release-policy.ts";
import { BASE_BRANCH } from "./require-limes.ts";

// BRANCH is the branch the release pull request is proposed from.
export const BRANCH = "release/next";
// RELEASE_SCOPE scopes the release commit and its pull request's title.
const RELEASE_SCOPE = "release";
// DEPENDENCIES_HEADING opens the section of an entry that names the limes a
// nested module now requires, as semantic-release's notes title it.
export const DEPENDENCIES_HEADING = "### Dependencies";
// TITLE_TAGS is how many tags a title names; more are counted.
const TITLE_TAGS = 3;
const GO_MOD = "go.mod";
const GO_SUM = "go.sum";
// CHANGELOGS are every module's changelog, as git add takes them.
const CHANGELOGS = ":(glob)**/CHANGELOG.md";

// Planned is one module's release in a plan.
export interface Planned {
  module: Module;
  // tag is the release's tag, its module's prefix first.
  tag: string;
  // entry is its changelog entry and its release's notes, heading first.
  entry: string;
  // raise: its requirement of limes is raised to the limes in the plan.
  raise: boolean;
  // major: it starts a major past the module's last release.
  major: boolean;
}

// Planner is what a plan is made from, the dry run injected so a test can
// stand in for semantic-release.
export interface Planner {
  root: string;
  repository: string;
  policy: Policy;
  commits: ReleaseCommits;
  // manual: asked for by hand, so the policy's thresholds do not apply.
  manual: boolean;
  // only names the one module asked for; undefined for every one.
  only: string | undefined;
  // today dates the notes a plan writes itself, YYYY-MM-DD.
  today: string;
  // dryRun is the notes module's next release would have, heading first,
  // or undefined when no commit since its last release releases anything.
  dryRun(module: Module): Promise<string | undefined>;
}

// planReleases is every release the checked-out commit is due, in the
// module table's order.
export async function planReleases(p: Planner): Promise<Planned[]> {
  const names = releasedModules(p.root);
  if (p.only !== undefined && !names.includes(p.only)) {
    throw new Error(`no module ${p.only}; known: ${names.join(", ")}`);
  }
  const plan: Planned[] = [];
  for (const name of names.filter((n) => p.only === undefined || n === p.only)) {
    const module = selectModule(p.root, name);
    if (!p.manual) {
      const due = isDue(await pendingCommits(module, "HEAD", p.commits, p.root), p.policy);
      info(`${name}: ${due.reason}`);
      if (!due.due) {
        continue;
      }
    }
    const notes = await p.dryRun(module);
    if (notes === undefined) {
      info(`${name}: no commit since its last release releases anything`);
      continue;
    }
    plan.push(await planned(p, module, notes, false));
  }
  const limes = plan.find((m) => m.module.name === ROOT_MODULE);
  if (limes !== undefined) {
    await raiseNested(p, plan, names, limes.tag);
  }
  const order = (m: Planned) => names.indexOf(m.module.name);
  return plan.sort((a, b) => order(a) - order(b));
}

// raiseNested adds to plan every nested module of names that requires a
// limes older than tag, its requirement raised to it.
async function raiseNested(p: Planner, plan: Planned[], names: readonly string[], tag: string): Promise<void> {
  for (const name of names.filter((n) => n !== ROOT_MODULE)) {
    const module = selectModule(p.root, name);
    const required = await requiredLimesOf(p.root, module);
    if (required === undefined || compareVersions(required, tag) >= 0) {
      continue;
    }
    const line = `* require limes ${tag}`;
    const known = plan.find((m) => m.module.name === name);
    if (known !== undefined) {
      known.entry = withDependency(known.entry, line);
      known.raise = true;
      continue;
    }
    const notes = (await p.dryRun(module)) ?? (await patchNotes(p, module));
    const entry = await planned(p, module, notes, true);
    entry.entry = withDependency(entry.entry, line);
    plan.push(entry);
  }
}

// requiredLimesOf is the limes module requires, undefined when it requires
// none or has no go.mod in the checkout.
async function requiredLimesOf(root: string, module: Module): Promise<string | undefined> {
  try {
    return await requiredLimes(join(root, module.dir));
  } catch {
    return undefined;
  }
}

// planned is module's release with notes.
async function planned(p: Planner, module: Module, notes: string, raise: boolean): Promise<Planned> {
  const entry = entryFrom(notes, "", "");
  const version = entryVersion(entry);
  if (version === undefined) {
    throw new Error(`${module.name}: the dry run's notes name no version: ${JSON.stringify(notes.split("\n")[0])}`);
  }
  const tag = `${module.prefix}v${version}`;
  const last = await lastStableTag(module, "HEAD", p.root);
  const major = last !== undefined && parseVersion(last, module.prefix).major < parseVersion(tag, module.prefix).major;
  return { module, tag, entry, raise, major };
}

// patchNotes is the notes of a patch of module that holds nothing but its
// requirement of limes: a heading in semantic-release's form.
async function patchNotes(p: Planner, module: Module): Promise<string> {
  const last = await lastStableTag(module, "HEAD", p.root);
  if (last === undefined) {
    throw new Error(`${module.name} has no release to cut a patch of`);
  }
  const version = nextVersion(parseVersion(last, module.prefix), Level.Patch);
  const compare = `https://github.com/${p.repository}/compare/${encodeURIComponent(last)}...${encodeURIComponent(`${module.prefix}v${version}`)}`;
  return `## [${version}](${compare}) (${p.today})\n`;
}

// withDependency is entry with line under its DEPENDENCIES_HEADING, the
// section added last when it has none.
export function withDependency(entry: string, line: string): string {
  const body = entry.trimEnd();
  const at = body.indexOf(`${DEPENDENCIES_HEADING}\n\n`);
  if (at < 0) {
    return `${body}\n\n${DEPENDENCIES_HEADING}\n\n${line}\n`;
  }
  const start = at + DEPENDENCIES_HEADING.length + 2;
  return `${body.slice(0, start)}${line}\n${body.slice(start)}\n`;
}

// releaseTitle is the subject of the release commit, and the title of its
// pull request, naming tags.
export function releaseTitle(tags: readonly string[]): string {
  const named = tags.length <= TITLE_TAGS ? tags.join(", ") : `${tags.length} releases`;
  return `chore(${RELEASE_SCOPE}): ${named}`;
}

// writePlan writes plan in the checkout at root: each raised requirement,
// the followers tidied, each entry first in its changelog.
async function writePlan(root: string, plan: readonly Planned[]): Promise<void> {
  const limes = plan.find((m) => m.module.name === ROOT_MODULE);
  const raised = plan.filter((m) => m.raise);
  if (limes !== undefined && raised.length > 0) {
    for (const m of raised) {
      await raise(root, m.module.dir, limes.tag);
    }
    await tidyFollowers(root);
  }
  for (const m of plan) {
    addEntry(root, m.module, m.entry);
  }
}

// Proposer is what proposing a release needs beside its plan.
export interface Proposer extends Planner {
  github: GitHub;
  // push pushes branch to the remote, as the token's owner.
  push(branch: string): Promise<void>;
}

// Proposed is the release pull request a run opened or updated, and whether
// it cuts a major, which waits for the maintainer's merge.
export interface Proposed {
  pullRequest: number;
  major: boolean;
}

// proposeRelease commits the plan of the checked-out commit on BRANCH and
// opens or updates its pull request; undefined when nothing is due.
export async function proposeRelease(ctx: Proposer): Promise<Proposed | undefined> {
  const plan = await planReleases(ctx);
  if (plan.length === 0) {
    info("no module is due a release");
    return undefined;
  }
  await git(["switch", "--quiet", "--discard-changes", "-C", BRANCH], ctx.root);
  await writePlan(ctx.root, plan);
  const tags = plan.map((m) => m.tag);
  const title = releaseTitle(tags);
  const modFiles = [...plan.filter((m) => m.raise).map((m) => m.module.dir), ...FOLLOWERS].flatMap((dir) => [join(dir, GO_MOD), join(dir, GO_SUM)]);
  await git(["add", "--all", "--", CHANGELOGS, ...modFiles.filter((f) => existsSync(join(ctx.root, f)))], ctx.root);
  await git(["commit", "--quiet", "-m", title], ctx.root);
  await ctx.push(BRANCH);
  const major = plan.some((m) => m.major);
  const text = { title, body: releaseBody(plan, major) };
  return { pullRequest: await openOrUpdate(ctx.github, text), major };
}

// releaseBody is the release pull request's description of plan.
function releaseBody(plan: readonly Planned[], major: boolean): string {
  const tags = plan.map((m) => `- ${m.tag}${m.raise ? " (requires the limes above)" : ""}`).join("\n");
  const merge = major ? "It cuts a major, so it waits for a merge by hand." : "auto-merge.yaml merges it once green.";
  return `Releases, once merged, every module below from the merge commit (docs/adr/0016-one-release-commit.md). ${merge}\n\n${tags}`;
}

// openOrUpdate proposes text on BRANCH: the open pull request from it,
// updated, or a new one.
async function openOrUpdate(github: GitHub, text: PullRequestText): Promise<number> {
  const open = await github.sameRepoPullRequest(BRANCH, BASE_BRANCH);
  if (open !== undefined) {
    await github.editPullRequest(open, text);
    info(`updated #${open}: ${text.title}`);
    return open;
  }
  const number = await github.createPullRequest(BRANCH, BASE_BRANCH, text);
  info(`opened #${number}: ${text.title}`);
  return number;
}
