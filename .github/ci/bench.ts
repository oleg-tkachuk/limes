// pgstore's benchmarks on this commit beside the newest pgstore release, on
// one runner, compared by benchstat, for the run's summary page.
//
// Usage: node bench.ts, from the repository's root, in a clone with its tags.
//
// A hosted runner's speed moves from one run to the next, so numbers are
// only compared within this run: the same benchmarks, on the release and on
// this commit, one after the other on the same machine. The release has no
// benchmarks of its own, so this commit's pgstore/bench_test.go is copied
// into a worktree of it; one that does not compile there is said so, and
// this commit's numbers are reported alone. Nothing here fails a run on a
// slower number: it is a report.

import { copyFileSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { info, summarize } from "./lib/actions.ts";
import { execute } from "./lib/exec.ts";
import { git, lines } from "./lib/git.ts";
import { runMain } from "./lib/main.ts";
import { isStableReleaseTag } from "./lib/releases.ts";

// PGSTORE is the module benchmarked, and the prefix of its release tags.
export const PGSTORE = "pgstore";
const PGSTORE_PREFIX = `${PGSTORE}/`;
// BENCH_FILE is the benchmarks, copied into the release's worktree.
export const BENCH_FILE = join(PGSTORE, "bench_test.go");
// BENCH_COUNT is how many times each benchmark runs on each side: enough
// for benchstat to tell a change from the runner's noise.
export const BENCH_COUNT = 10;
// BENCH_TIME is how long one run of a benchmark takes.
export const BENCH_TIME = "1s";
// BENCH_TIMEOUT bounds one side's whole run.
export const BENCH_TIMEOUT = "30m";
// TOOLS_MODFILE is the module that pins benchstat.
const TOOLS_MODFILE = "tools/go.mod";

// benchArgs is the go command that runs every benchmark of a package and no
// test.
export function benchArgs(): string[] {
  return ["test", "-run", "^$", "-bench", ".", "-count", String(BENCH_COUNT), "-benchtime", BENCH_TIME, "-timeout", BENCH_TIMEOUT, "."];
}

// newestRelease is the newest stable release tag of pgstore among tags,
// which git lists newest version first.
export function newestRelease(tags: readonly string[]): string | undefined {
  return tags.find((t) => isStableReleaseTag(t, PGSTORE_PREFIX));
}

// Comparison is what one run found.
export interface Comparison {
  // base is the release compared against; undefined when there is none.
  base: string | undefined;
  // baseProblem says why the release could not be benchmarked, if so.
  baseProblem: string | undefined;
  // table is benchstat's output.
  table: string;
}

// report is the Markdown of a comparison for the run's summary page.
export function report(c: Comparison): string {
  const against =
    c.base === undefined
      ? "No pgstore release to compare against: this commit's numbers alone."
      : c.baseProblem === undefined
        ? `This commit against \`${c.base}\`, on this runner, ${BENCH_COUNT} runs of each. benchstat marks a difference it holds significant; \`~\` is none.`
        : `\`${c.base}\` is not comparable (${c.baseProblem}): this commit's numbers alone.`;
  return `### pgstore benchmarks\n\n${against}\n\n\`\`\`text\n${c.table.trimEnd()}\n\`\`\`\n`;
}

// Bench is what a run does to the outside, injected so a test stands in.
export interface Bench {
  // tags is every tag of the clone, newest version first.
  tags(): Promise<string[]>;
  // run runs the benchmarks of the pgstore directory of the checkout at
  // root and returns their output.
  run(root: string): Promise<string>;
  // checkout makes a worktree of tag and returns its root.
  checkout(tag: string): Promise<string>;
  // remove removes a worktree checkout made.
  remove(worktree: string): Promise<void>;
  // benchstat compares the outputs, base first when there is one, each
  // headed by its label.
  benchstat(outputs: string[], labels: string[]): Promise<string>;
}

// HEAD_LABEL heads this commit's column.
export const HEAD_LABEL = "HEAD";

// compareBenchmarks benchmarks this commit and the newest release.
export async function compareBenchmarks(b: Bench, root: string): Promise<Comparison> {
  const head = await b.run(root);
  const base = newestRelease(await b.tags());
  if (base === undefined) {
    return { base, baseProblem: undefined, table: await b.benchstat([head], [HEAD_LABEL]) };
  }
  const worktree = await b.checkout(base);
  try {
    copyFileSync(join(root, BENCH_FILE), join(worktree, BENCH_FILE));
    let baseOut: string;
    try {
      baseOut = await b.run(worktree);
    } catch (err) {
      const problem = `the benchmarks did not run there: ${err instanceof Error ? err.message.split("\n")[0] : String(err)}`;
      return { base, baseProblem: problem, table: await b.benchstat([head], [HEAD_LABEL]) };
    }
    return { base, baseProblem: undefined, table: await b.benchstat([baseOut, head], [base, HEAD_LABEL]) };
  } finally {
    await b.remove(worktree);
  }
}

// bench is the Bench over git, go and benchstat; out is where the outputs
// are written for benchstat to read.
function bench(root: string, out: string): Bench {
  return {
    tags: async () => lines(await git(["tag", "--list", `${PGSTORE_PREFIX}v*`, "--sort=-v:refname"], root)),
    run: (dir) => execute("go", benchArgs(), { cwd: join(dir, PGSTORE) }),
    checkout: async (tag) => {
      const worktree = join(out, "base");
      await git(["worktree", "add", "--detach", worktree, tag], root);
      return worktree;
    },
    remove: async (worktree) => void (await git(["worktree", "remove", "--force", worktree], root)),
    benchstat: async (outputs, labels) => {
      // benchstat heads each column with the label an input names, as
      // label=file.
      const inputs = outputs.map((o, i) => {
        const file = join(out, `${i}.txt`);
        writeFileSync(file, o);
        return `${labels[i] ?? String(i)}=${file}`;
      });
      return execute("go", ["tool", `-modfile=${TOOLS_MODFILE}`, "benchstat", ...inputs], { cwd: root });
    },
  };
}

async function main(): Promise<void> {
  const root = (await git(["rev-parse", "--show-toplevel"])).trim();
  const out = mkdtempSync(join(tmpdir(), "limes-bench-"));
  try {
    const c = await compareBenchmarks(bench(root, out), root);
    info(c.table);
    summarize(report(c));
  } finally {
    rmSync(out, { recursive: true, force: true });
  }
}

if (import.meta.main) {
  await runMain(main);
}
