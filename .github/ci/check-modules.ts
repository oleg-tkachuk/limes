// Run a release check over every module the repository releases.
//
// Usage: EVENT=… PR_BASE=… PR_HEAD=… SHA=… [PR_TITLE=…] node check-modules.ts api|markers
//
// For each module in .github/release/modules.cjs, names the commits to check
// (checkRange) and runs the API or the marker check over them. A module with
// nothing to check from — a push before its first release — is skipped.
// Every module is checked, and the run fails when any check did. The marker
// check also reads PR_TITLE, the title that becomes the merge commit's body.

import { error, info } from "./lib/actions.ts";
import { optionalEnv, requireEnv } from "./lib/env.ts";
import { git } from "./lib/git.ts";
import { runMain } from "./lib/main.ts";
import { releaseCommits, releasedModules, selectModule, type Module } from "./lib/modules.ts";
import { apiBreakCheck, apidiffCommand, checkRange, markerCheck, type Verdict } from "./release-checks.ts";

// Check names the check a run asks for.
export const Check = { Api: "api", Markers: "markers" } as const;
export type Check = (typeof Check)[keyof typeof Check];

// Event is what the workflow says about the change.
export interface Event {
  name: string;
  prBase: string;
  prHead: string;
  sha: string;
}

// ModuleCheck checks one module over base..head.
export type ModuleCheck = (module: Module, base: string, head: string) => Promise<Verdict>;

// checkModules runs check over every module of the checkout at root, and
// reports whether every one passed.
export async function checkModules(root: string, event: Event, check: ModuleCheck): Promise<boolean> {
  let passed = true;
  for (const name of releasedModules(root)) {
    const module = selectModule(root, name);
    const range = await checkRange(module, event.name, event.prBase, event.prHead, event.sha, root);
    if (range.base === undefined) {
      info(`${name}: no release to check from yet`);
      continue;
    }
    const verdict = await check(module, range.base, range.head);
    if (verdict.ok) {
      info(verdict.message);
    } else {
      error(verdict.message);
      passed = false;
    }
  }
  return passed;
}

// moduleCheck is the check a run named, over the checkout at root.
export function moduleCheck(check: string, root: string, title: string | undefined, apidiff: string | undefined): ModuleCheck {
  const commits = releaseCommits();
  switch (check) {
    case Check.Api: {
      const command = apidiffCommand(root, apidiff);
      return (module, base, head) => apiBreakCheck(module, base, head, command, commits, root);
    }
    case Check.Markers:
      return (module, base, head) => markerCheck(module, base, head, title, commits, root);
    default:
      throw new Error(`usage: check-modules.ts ${Check.Api}|${Check.Markers}`);
  }
}

async function main(): Promise<void> {
  const root = (await git(["rev-parse", "--show-toplevel"])).trim();
  const check = moduleCheck(process.argv[2] ?? "", root, optionalEnv("PR_TITLE"), optionalEnv("APIDIFF"));
  const event: Event = {
    name: requireEnv("EVENT"),
    prBase: optionalEnv("PR_BASE") ?? "",
    prHead: optionalEnv("PR_HEAD") ?? "",
    sha: requireEnv("SHA"),
  };
  if (!(await checkModules(root, event, check))) {
    process.exitCode = 1;
  }
}

if (import.meta.main) {
  await runMain(main);
}
