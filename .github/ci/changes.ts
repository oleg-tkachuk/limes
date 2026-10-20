// Which change does this CI run check, and which of the checks does it reach?
//
// A pull request names both ends of its diff (BASE, HEAD); a push names the
// branch's previous head (BEFORE) and its new one (AFTER). The step sets one
// output per group of checks (Group), true when the change reaches it, and
// `relevant`, false when every changed path is documentation, so the checks
// after it report as skipped; `verify-tasks` names what the Verify job runs.
//
// A path reaches the group whose directory holds it. One that decides what
// CI runs — a workflow, the setup action, the Taskfile, a pinned tool, the
// root go.mod — reaches every group, and so does a path no group claims and
// that is not documentation: it is the root module's code, or something
// nobody has classified yet, and either is checked by default, not skipped.
//
// A push that created the branch reports an all-zero BEFORE, a force-push can
// name a commit this clone does not have, and a run by hand names none at
// all. None of them is a diff, so each reaches everything.
//
// A push to main checks everything, whatever its diff reaches: its run is
// what a release is vouched for by (.github/ci/ci-run.ts) and what says
// whether main is green (main-health.ts), and a run that skipped a check
// would pass for code it never tested — the code commit under a
// documentation-only one, say, whose own run failed. The diff still decides
// `code`, whether the push changed anything but documentation, which is what
// a release is dispatched on.

import { info, setOutput } from "./lib/actions.ts";
import { optionalEnv } from "./lib/env.ts";
import { changedPaths, hasCommit } from "./lib/git.ts";
import { matchesAny } from "./lib/glob.ts";
import { runMain } from "./lib/main.ts";
import { definesCi } from "./merge-policy.ts";

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

// Group names a set of checks a change can reach, and the step output that
// says whether it does.
export const Group = {
  // Root is the limes module: its code, and anything unclassified.
  Root: "root",
  Pgstore: "pgstore",
  Redisreplay: "redisreplay",
  Otelmetric: "otelmetric",
  Examples: "examples",
  // Scripts is the TypeScript the workflows run and the release tooling.
  Scripts: "scripts",
} as const;
export type Group = (typeof Group)[keyof typeof Group];

// GROUP_DIRS is the directory each group but the root holds. Every nested
// module of .github/release/modules.cjs has a group: module-lists.test.ts.
export const GROUP_DIRS: Readonly<Record<Exclude<Group, typeof Group.Root>, readonly string[]>> = {
  pgstore: ["pgstore/"],
  redisreplay: ["redisreplay/"],
  otelmetric: ["otelmetric/"],
  examples: ["examples/"],
  scripts: [".github/ci/", ".github/release/"],
};

// ALL_GROUPS is every group, for a change that reaches everything.
export const ALL_GROUPS: readonly Group[] = Object.values(Group);

// OUTPUT_RELEVANT is the output that says the change is more than
// documentation; OUTPUT_VERIFY_TASKS, the tasks the Verify job runs.
export const OUTPUT_RELEVANT = "relevant";
// OUTPUT_CODE says the diff itself reaches more than documentation, whatever
// the run checks; ci.yaml's dispatch-release reads it.
export const OUTPUT_CODE = "code";
// PUSH_EVENT is the event of a run on main after a merge or a push.
export const PUSH_EVENT = "push";
export const OUTPUT_VERIFY_TASKS = "verify-tasks";
// VERIFY_GATE is the whole gate but golangci-lint, which CI runs as a job of
// its own; SCRIPTS_TASKS, its part over the scripts.
export const VERIFY_GATE = "verify:gate";
export const SCRIPTS_TASKS = "verify:ci verify:release-rules";

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

// READ_BY_TESTS are the inert-looking paths a test reads, with the group
// whose checks run it: the README's five-minute walkthrough runs as
// examples/internal/readme's test. changes.test.ts fails when a test reads a
// path of INERT that is not listed here.
export const READ_BY_TESTS: Readonly<Record<string, Group>> = { "README.md": Group.Examples };

// groupsOf is the groups one path reaches.
export function groupsOf(path: string): readonly Group[] {
  const readBy = READ_BY_TESTS[path];
  if (readBy !== undefined) {
    return [readBy];
  }
  if (matchesAny(path, INERT)) {
    return [];
  }
  for (const [group, dirs] of Object.entries(GROUP_DIRS)) {
    if (dirs.some((d) => path.startsWith(d))) {
      return [group as Group];
    }
  }
  return definesCi(path) ? ALL_GROUPS : [Group.Root];
}

// reachedGroups is every group a change of paths reaches.
export function reachedGroups(paths: readonly string[]): Set<Group> {
  return new Set(paths.flatMap(groupsOf));
}

// verifyTasks is what the Verify job runs for groups: the scripts' part
// alone when nothing else is reached, else the whole gate.
export function verifyTasks(groups: ReadonlySet<Group>): string {
  return groups.size === 1 && groups.has(Group.Scripts) ? SCRIPTS_TASKS : VERIFY_GATE;
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

// classify is the groups the change reaches: of every path of the diff the
// event names, or every group when there is no diff this clone can take.
export async function classify(env: DiffEnv, cwd?: string): Promise<Set<Group>> {
  const range = candidateRange(env);
  if (range === undefined || !(await hasCommit(range.from, cwd))) {
    info(`no usable base (${range?.from ?? env.before ?? "none"}) — running everything`);
    return new Set(ALL_GROUPS);
  }
  const paths = await changedPaths(range.from, range.to, cwd);
  info(paths.join("\n"));
  return reachedGroups(paths);
}

// checkedGroups is the groups a run of event checks, given those its diff
// reaches: every one for a push, the reached ones otherwise.
export function checkedGroups(event: string | undefined, reached: ReadonlySet<Group>): Set<Group> {
  return event === PUSH_EVENT ? new Set(ALL_GROUPS) : new Set(reached);
}

async function main(): Promise<void> {
  const reached = await classify({
    base: optionalEnv("BASE"),
    head: optionalEnv("HEAD"),
    before: optionalEnv("BEFORE"),
    after: optionalEnv("AFTER"),
  });
  const event = optionalEnv("EVENT");
  const groups = checkedGroups(event, reached);
  if (event === PUSH_EVENT) {
    info("a push to main checks everything, whatever its diff reaches");
  }
  if (groups.size === 0) {
    info("documentation only — the checks below will report as skipped");
  } else {
    info(`reaches: ${[...groups].join(", ")}`);
  }
  setOutput(OUTPUT_RELEVANT, String(groups.size > 0));
  setOutput(OUTPUT_CODE, String(reached.size > 0));
  for (const group of ALL_GROUPS) {
    setOutput(group, String(groups.has(group)));
  }
  setOutput(OUTPUT_VERIFY_TASKS, verifyTasks(groups));
}

if (import.meta.main) {
  await runMain(main);
}
