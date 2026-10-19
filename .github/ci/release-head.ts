// Is the checked-out head the commit CI passed?
//
// Usage: node release-head.ts check <dispatched-sha>
//        node release-head.ts catch-up <dispatched-sha>
//
// Sets the step output `release` to true when HEAD is the dispatched commit,
// and to false when main has moved past it. release.yaml checks out the
// branch head, because semantic-release releases only from a branch, and the
// head can be newer than the commit whose CI dispatched the run: a pull
// request merged while that CI finished. Tagging it would release a commit
// whose own checks had not passed. When the head has moved, this run releases
// nothing: the newer commit's CI dispatches its own run once it passes, and
// semantic-release counts this commit's changes in that release.
//
// Unless everything past the dispatched commit is documentation (changes.ts):
// CI dispatches no release for such a commit, so waiting for it would leave
// this one unreleased until the next code change. Its code is the dispatched
// commit's, byte for byte, so the head is released.
//
// catch-up first fast-forwards the checkout to main's head: semantic-release
// refuses to publish from a branch behind its remote, and exits 0 when it
// does, so a commit landing on main between the checkout and semantic-release
// would drop the release without a word.
//
// An empty sha is an error, not a pass: the workflow cannot tell which commit
// CI vouched for, so it does not release.

import { info, setOutput } from "./lib/actions.ts";
import { changedPaths, git } from "./lib/git.ts";
import { runMain } from "./lib/main.ts";
import { reachesCode } from "./changes.ts";

// OUTPUT_RELEASE is the step output release.yaml's later steps read.
export const OUTPUT_RELEASE = "release";

// REMOTE and BRANCH are where the release catches up from.
const REMOTE = "origin";
const BRANCH = "main";

// Command is what the step asks for.
export const Command = { Check: "check", CatchUp: "catch-up" } as const;

// resolveCommit is the full sha a name resolves to in this clone, or
// undefined when the clone has no such commit.
async function resolveCommit(name: string, cwd?: string): Promise<string | undefined> {
  try {
    return (await git(["rev-parse", "--verify", "--quiet", `${name}^{commit}`], cwd)).trim();
  } catch {
    return undefined;
  }
}

// isAncestor reports whether ancestor is in descendant's history.
async function isAncestor(ancestor: string, descendant: string, cwd?: string): Promise<boolean> {
  try {
    await git(["merge-base", "--is-ancestor", ancestor, descendant], cwd);
    return true;
  } catch {
    return false;
  }
}

// releasable decides whether the checked-out head may be released for the
// commit CI passed, sha.
export async function releasable(sha: string, cwd?: string): Promise<boolean> {
  if (sha === "") {
    throw new Error("no dispatched sha: cannot tell which commit CI passed");
  }
  const head = (await git(["rev-parse", "HEAD"], cwd)).trim();
  const dispatched = await resolveCommit(sha, cwd);
  if (dispatched === head) {
    return true;
  }
  if (dispatched !== undefined && (await isAncestor(dispatched, head, cwd)) && !reachesCode(await changedPaths(dispatched, head, cwd))) {
    info(`main moved past ${sha} to ${head} with documentation only; releasing the head, whose code is ${sha}'s`);
    return true;
  }
  info(`main moved past ${sha} to ${head}; that commit's own CI releases it`);
  return false;
}

// catchUp fast-forwards the checkout to the remote branch's head.
export async function catchUp(cwd?: string): Promise<void> {
  await git(["fetch", "--quiet", REMOTE, BRANCH], cwd);
  const head = (await git(["rev-parse", "HEAD"], cwd)).trim();
  const fetched = (await git(["rev-parse", "FETCH_HEAD"], cwd)).trim();
  if (head !== fetched) {
    await git(["merge", "--quiet", "--ff-only", "FETCH_HEAD"], cwd);
    info(`main moved to ${fetched} since the checkout`);
  }
}

async function main(): Promise<void> {
  const [command, sha = ""] = process.argv.slice(2);
  switch (command) {
    case Command.CatchUp:
      await catchUp();
      break;
    case Command.Check:
      break;
    default:
      throw new Error(`usage: release-head.ts ${Command.Check}|${Command.CatchUp} <dispatched-sha>`);
  }
  setOutput(OUTPUT_RELEASE, String(await releasable(sha)));
}

if (import.meta.main) {
  await runMain(main);
}
