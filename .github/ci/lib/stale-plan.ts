// Whether a release plan still describes the code it would release
// (docs/adr/0016-one-release-commit.md, "Stale plans").
//
// A release commit is one commit on the release branch, cut from main: its
// parent is the plan's base, the commit whose history the plan counted. A
// commit that reached main after the base, and that a module's rules
// release, is in the code the merge of that pull request tags, but not in
// the versions the plan wrote: a breaking change after a plan of a patch
// would be tagged as the patch. Such a commit stales the plan when its
// module is in the plan, or when it makes a module the plan leaves out due
// by the policy. Commits no rules release — documentation, chores, the
// release commits themselves — stale nothing, and neither does a commit
// below the policy's thresholds for a module left out: the next plan counts
// it.

import { changelogPath } from "../changelog.ts";
import { code } from "./actions.ts";
import { changedPaths, git, lines } from "./git.ts";
import { releasedModules, selectModule, type ReleaseCommits } from "./modules.ts";
import { isDue, pendingCommits, releasingCommits, type Pending, type Policy } from "./release-policy.ts";

// RELEASE_BRANCH is the branch the release pull request is proposed from.
export const RELEASE_BRANCH = "release/next";

// SHORT_SHA is how many characters of a commit id a message prints.
export const SHORT_SHA = 7;

// RELEASE_COMMIT_PARENTS is how many parents a release commit has: it is
// one commit on main's history.
const RELEASE_COMMIT_PARENTS = 1;

// Based is a release commit's plan base, or why it cannot be told.
export type Based = { base: string; unknown?: undefined } | { base?: undefined; unknown: string };

// parentsOf is the parents of commit, first parent first.
export async function parentsOf(commit: string, root: string): Promise<string[]> {
  const [, ...parents] = lines(await git(["rev-list", "--parents", "--max-count=1", commit], root))[0]?.split(" ") ?? [];
  return parents;
}

// isAncestor reports whether ancestor is in descendant's history.
async function isAncestor(ancestor: string, descendant: string, root: string): Promise<boolean> {
  try {
    await git(["merge-base", "--is-ancestor", ancestor, descendant], root);
    return true;
  } catch {
    return false;
  }
}

// planBaseOf is the base of the plan the release commit holds, which head —
// main, when it is judged — must descend from: a release commit of more
// parents than one, or one cut from history head has not, is no plan this
// can judge.
export async function planBaseOf(release: string, head: string, root: string): Promise<Based> {
  const parents = await parentsOf(release, root);
  const base = parents[0];
  if (parents.length !== RELEASE_COMMIT_PARENTS || base === undefined) {
    return { unknown: `release commit ${short(release)} has ${parents.length} parents, not ${RELEASE_COMMIT_PARENTS}` };
  }
  if (!(await isAncestor(base, head, root))) {
    return { unknown: `the plan's base ${short(base)} is not in the history of ${short(head)}` };
  }
  return { base };
}

// Staled is a module whose release the commits after a plan's base change:
// one in the plan, or one they make due.
export interface Staled {
  module: string;
  planned: boolean;
  commits: Pending[];
}

// Staleness is what judging a plan needs: the checkout at root, the release
// commit and its base, the head the plan is judged against, and the
// release's own counting.
export interface Staleness {
  root: string;
  release: string;
  base: string;
  head: string;
  commits: ReleaseCommits;
  policy: Policy;
}

// plannedModules is every module whose changelog the release commit
// changes: the modules its plan releases.
export async function plannedModules(root: string, base: string, release: string): Promise<string[]> {
  const changed = new Set(await changedPaths(base, release, root));
  return releasedModules(root).filter((name) => changed.has(changelogPath(selectModule(root, name))));
}

// staleModules is every module the commits from base to head stale the
// plan of, in the module table's order; none when head is the base.
export async function staleModules(s: Staleness): Promise<Staled[]> {
  if ((await git(["rev-parse", s.base], s.root)).trim() === (await git(["rev-parse", s.head], s.root)).trim()) {
    return [];
  }
  const planned = await plannedModules(s.root, s.base, s.release);
  const staled: Staled[] = [];
  for (const name of releasedModules(s.root)) {
    const module = selectModule(s.root, name);
    const since = await releasingCommits(module, `${s.base}..${s.head}`, s.commits, s.root);
    if (since.length === 0) {
      continue;
    }
    if (planned.includes(name)) {
      staled.push({ module: name, planned: true, commits: since });
      continue;
    }
    if (isDue(await pendingCommits(module, s.head, s.commits, s.root), s.policy).due) {
      staled.push({ module: name, planned: false, commits: since });
    }
  }
  return staled;
}

// staleLines is staled as lines of Markdown, one a commit and module, the
// subject a code span: a commit's author wrote it.
export function staleLines(staled: readonly Staled[]): string[] {
  return staled.flatMap((s) =>
    s.commits.map((c) => `- ${code(short(c.hash))} ${code(c.subject)} — releases ${s.module} (${c.level}), ${s.planned ? "which the plan releases" : "which the plan leaves out and it makes due"}`),
  );
}

// short is a commit id as a message prints it.
export function short(sha: string): string {
  return sha.slice(0, SHORT_SHA);
}
