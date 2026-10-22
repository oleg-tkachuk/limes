// Run a release check over every module the repository releases.
//
// Usage: EVENT=… PR_BASE=… PR_HEAD=… SHA=… [PR_TITLE=…] node check-modules.ts api|markers
//
// For each module in .github/release/modules.cjs, names the commits to check
// (checkRange) and runs the API or the marker check over them. A module with
// nothing to check from — a push before its first release — is skipped.
// Every module is checked, and the run fails when any check did; each
// module's verdict goes on the run's summary page. The marker check also
// reads PR_TITLE, the title that becomes the merge commit's body.

import { error, info, summarize, table } from "./lib/actions.ts";
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

// Outcome is what a check found of one module: a pass, a failure, or
// nothing to check from.
export const Outcome = { Passed: "passed", Failed: "failed", Skipped: "skipped" } as const;
export type Outcome = (typeof Outcome)[keyof typeof Outcome];

// ModuleResult is one module's outcome and what the check said of it.
export interface ModuleResult {
  module: string;
  outcome: Outcome;
  message: string;
}

// NO_RELEASE is what a module never released is skipped with.
const NO_RELEASE = "no release to check from yet";

// checkModules runs check over every module of the checkout at root, in the
// table's order, and says what it found of each.
export async function checkModules(root: string, event: Event, check: ModuleCheck): Promise<ModuleResult[]> {
  const results: ModuleResult[] = [];
  for (const name of releasedModules(root)) {
    const module = selectModule(root, name);
    const range = await checkRange(module, event.name, event.prBase, event.prHead, event.sha, root);
    if (range.base === undefined) {
      info(`${name}: ${NO_RELEASE}`);
      results.push({ module: name, outcome: Outcome.Skipped, message: NO_RELEASE });
      continue;
    }
    const verdict = await check(module, range.base, range.head);
    if (verdict.ok) {
      info(verdict.message);
    } else {
      error(verdict.message);
    }
    results.push({ module: name, outcome: verdict.ok ? Outcome.Passed : Outcome.Failed, message: verdict.message });
  }
  return results;
}

// allPassed reports whether no module failed: one skipped had nothing to
// check.
export function allPassed(results: readonly ModuleResult[]): boolean {
  return results.every((r) => r.outcome !== Outcome.Failed);
}

// TITLES heads each check's section of the summary page, as its job is
// named; SUMMARY_HEADER heads the table under it.
const TITLES: Readonly<Record<Check, string>> = { [Check.Api]: "API compatibility", [Check.Markers]: "Breaking markers" };
const SUMMARY_HEADER = ["Module", "Result", "Detail"];

// checkSummary is the summary page section of check over results.
export function checkSummary(check: Check, results: readonly ModuleResult[]): string {
  return `### ${TITLES[check]}\n\n${table(SUMMARY_HEADER, results.map((r) => [r.module, r.outcome, r.message]))}`;
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
  const name = process.argv[2] ?? "";
  const check = moduleCheck(name, root, optionalEnv("PR_TITLE"), optionalEnv("APIDIFF"));
  const event: Event = {
    name: requireEnv("EVENT"),
    prBase: optionalEnv("PR_BASE") ?? "",
    prHead: optionalEnv("PR_HEAD") ?? "",
    sha: requireEnv("SHA"),
  };
  const results = await checkModules(root, event, check);
  summarize(checkSummary(name as Check, results));
  if (!allPassed(results)) {
    process.exitCode = 1;
  }
}

if (import.meta.main) {
  await runMain(main);
}
