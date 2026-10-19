// Which change does this CI run check, and does it reach anything CI checks?
//
// A pull request names both ends of its diff (BASE, HEAD); a push names the
// branch's previous head (BEFORE) and its new one (AFTER). The step's output
// `relevant` is false when every changed path is documentation, so the checks
// after it report as skipped.
//
// A push that created the branch reports an all-zero BEFORE, a force-push can
// name a commit this clone does not have, and a run by hand names none at
// all. None of them is a diff, so each means everything.

import { info, setOutput } from "./lib/actions.ts";
import { optionalEnv } from "./lib/env.ts";
import { changedPaths, hasCommit } from "./lib/git.ts";
import { matchesAny } from "./lib/glob.ts";
import { runMain } from "./lib/main.ts";

// INERT are the paths that change nothing CI checks, as GitHub's path filters
// spell them. An EXCLUSION list, and the direction matters: a file type nobody
// has thought about yet is relevant by default. codeql.yaml's paths-ignore is
// this list; changes.test.ts fails when the two disagree.
export const INERT: readonly string[] = [
  "**/*.md",
  "LICENSE",
  "NOTICE",
  ".gitignore",
  "**/*.png",
  "**/*.jpg",
  "**/*.jpeg",
  "**/*.svg",
  "**/*.gif",
];

// OUTPUT_RELEVANT is the step output ci.yaml's jobs read.
export const OUTPUT_RELEVANT = "relevant";

// ZERO_SHA is the BEFORE of a push that created its branch.
const ZERO_SHA = "0".repeat(40);

// Range is the two commits a diff runs between.
export interface Range {
  from: string;
  to: string;
}

// DiffEnv is what the event says about the change.
export interface DiffEnv {
  base?: string | undefined;
  head?: string | undefined;
  before?: string | undefined;
  after?: string | undefined;
}

// reachesCode reports whether any of paths is outside INERT.
export function reachesCode(paths: readonly string[]): boolean {
  return paths.some((path) => !matchesAny(path, INERT));
}

// candidateRange is the diff the event names: a pull request's ends win over a
// push's. Undefined when it names no two ends.
export function candidateRange(env: DiffEnv): Range | undefined {
  const [from, to] = env.base !== undefined ? [env.base, env.head] : [env.before, env.after];
  if (from === undefined || to === undefined || from === ZERO_SHA) {
    return undefined;
  }
  return { from, to };
}

// classify decides whether the change reaches code: every path of the diff
// the event names, or everything when there is no diff this clone can take.
export async function classify(env: DiffEnv, cwd?: string): Promise<boolean> {
  const range = candidateRange(env);
  if (range === undefined || !(await hasCommit(range.from, cwd))) {
    info(`no usable base (${range?.from ?? env.before ?? "none"}) — running everything`);
    return true;
  }
  const paths = await changedPaths(range.from, range.to, cwd);
  info(paths.join("\n"));
  return reachesCode(paths);
}

async function main(): Promise<void> {
  const relevant = await classify({
    base: optionalEnv("BASE"),
    head: optionalEnv("HEAD"),
    before: optionalEnv("BEFORE"),
    after: optionalEnv("AFTER"),
  });
  if (!relevant) {
    info("documentation only — the checks below will report as skipped");
  }
  setOutput(OUTPUT_RELEVANT, String(relevant));
}

if (import.meta.main) {
  await runMain(main);
}
