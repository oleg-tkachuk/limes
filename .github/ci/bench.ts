// A module's benchmarks on this commit beside the module's newest release,
// on one runner, compared by benchstat, for the run's summary page.
//
// Usage: node bench.ts <module>, from the repository's root, in a clone with
// its tags; the module is one of BENCHED, by its name in
// .github/release/modules.cjs.
//
// A hosted runner's speed moves from one run to the next, so numbers are
// only compared within this run: the same benchmarks, on the release and on
// this commit, taking turns on the same machine — one round of each, then
// the next — so a slow spell lands on both sides rather than on one. The release has no
// benchmarks of its own, so this commit's bench_test.go of the module is
// copied into a worktree of it; one that does not compile there is said so,
// and this commit's numbers are reported alone. Nothing here fails a run on
// a slower number: it is a report.

import { copyFileSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { info, summarize } from "./lib/actions.ts";
import { execute } from "./lib/exec.ts";
import { git, lines } from "./lib/git.ts";
import { runMain } from "./lib/main.ts";
import { ROOT_MODULE, selectModule, type Module } from "./lib/modules.ts";
import { isStableReleaseTag } from "./lib/releases.ts";

// BENCHED are the modules with benchmarks to compare; bench.yaml's matrix
// runs each (bench.test.ts).
export const BENCHED: readonly string[] = [ROOT_MODULE, "pgstore", "otelmetric"];
// BENCH_FILE_NAME is a module's benchmarks, copied into the release's
// worktree.
const BENCH_FILE_NAME = "bench_test.go";
// BENCH_ROUNDS is how many times each benchmark runs on each side, a round
// of each side in turn: enough for benchstat to tell a change from the
// runner's noise.
export const BENCH_ROUNDS = 10;
// BENCH_TIME is how long one run of a benchmark takes.
export const BENCH_TIME = "1s";
// BENCH_TIMEOUT bounds one side's whole run.
export const BENCH_TIMEOUT = "30m";
// TOOLS_MODFILE is the module that pins benchstat.
const TOOLS_MODFILE = "tools/go.mod";
// HEAD_LABEL heads this commit's column.
export const HEAD_LABEL = "HEAD";

// Benched is what a comparison needs of a module: its directory and the
// prefix of its release tags.
export type Benched = Pick<Module, "dir" | "prefix">;

// benchFile is module's benchmarks, from the repository's root.
export function benchFile(module: Pick<Module, "dir">): string {
  return join(module.dir, BENCH_FILE_NAME);
}

// benchArgs is the go command that runs every benchmark of a package once
// and no test: one round.
export function benchArgs(): string[] {
  return ["test", "-run", "^$", "-bench", ".", "-count", "1", "-benchtime", BENCH_TIME, "-timeout", BENCH_TIMEOUT, "."];
}

// newestRelease is the newest stable release tag of the module whose tags
// start with prefix among tags, which git lists newest version first.
export function newestRelease(tags: readonly string[], prefix: string): string | undefined {
  return tags.find((t) => isStableReleaseTag(t, prefix));
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

// report is the Markdown of module name's comparison for the run's summary
// page.
export function report(name: string, c: Comparison): string {
  const against =
    c.base === undefined
      ? `No ${name} release to compare against: this commit's numbers alone.`
      : c.baseProblem === undefined
        ? `This commit against \`${c.base}\`, on this runner, ${BENCH_ROUNDS} rounds of each, taking turns. benchstat marks a difference it holds significant; \`~\` is none.`
        : `\`${c.base}\` is not comparable (${c.baseProblem}): this commit's numbers alone.`;
  return `### ${name} benchmarks\n\n${against}\n\n\`\`\`text\n${c.table.trimEnd()}\n\`\`\`\n`;
}

// Bench is what a run does to the outside, injected so a test stands in.
export interface Bench {
  // tags is every tag of the clone, newest version first.
  tags(): Promise<string[]>;
  // run runs one round of the benchmarks of the module's directory of the
  // checkout at root and returns its output.
  run(root: string): Promise<string>;
  // checkout makes a worktree of tag and returns its root.
  checkout(tag: string): Promise<string>;
  // remove removes a worktree checkout made.
  remove(worktree: string): Promise<void>;
  // benchstat compares the outputs, base first when there is one, each
  // headed by its label.
  benchstat(outputs: string[], labels: string[]): Promise<string>;
}

// compareBenchmarks benchmarks module on this commit and at its newest
// release, BENCH_ROUNDS rounds of each, in turn.
export async function compareBenchmarks(b: Bench, root: string, module: Benched): Promise<Comparison> {
  const base = newestRelease(await b.tags(), module.prefix);
  if (base === undefined) {
    return { base, baseProblem: undefined, table: await b.benchstat([await rounds(b, root)], [HEAD_LABEL]) };
  }
  const worktree = await b.checkout(base);
  try {
    copyFileSync(join(root, benchFile(module)), join(worktree, benchFile(module)));
    let head = "";
    let baseOut = "";
    for (let round = 0; round < BENCH_ROUNDS; round++) {
      head += await b.run(root);
      try {
        baseOut += await b.run(worktree);
      } catch (err) {
        // The release cannot run them: this commit's rounds alone.
        const problem = `the benchmarks did not run there: ${err instanceof Error ? err.message.split("\n")[0] : String(err)}`;
        return { base, baseProblem: problem, table: await b.benchstat([head + (await rounds(b, root, round + 1))], [HEAD_LABEL]) };
      }
    }
    return { base, baseProblem: undefined, table: await b.benchstat([baseOut, head], [base, HEAD_LABEL]) };
  } finally {
    await b.remove(worktree);
  }
}

// rounds runs the benchmarks of root the rounds left after done.
async function rounds(b: Bench, root: string, done = 0): Promise<string> {
  let out = "";
  for (let round = done; round < BENCH_ROUNDS; round++) {
    out += await b.run(root);
  }
  return out;
}

// FAILURE_LINES is how much of a failed run's output its error carries.
const FAILURE_LINES = 20;

// outputOf is the standard output a failed command printed, if any.
function outputOf(err: unknown): string {
  const cause = err instanceof Error ? err.cause : undefined;
  return typeof cause === "object" && cause !== null && "stdout" in cause ? String(cause.stdout) : "";
}

// tail is the last FAILURE_LINES lines of output.
export function tail(output: string): string {
  return output.trimEnd().split("\n").slice(-FAILURE_LINES).join("\n");
}

// bench is the Bench of module over git, go and benchstat; out is where the
// outputs are written for benchstat to read.
function bench(root: string, out: string, module: Benched): Bench {
  return {
    tags: async () => lines(await git(["tag", "--list", `${module.prefix}v*`, "--sort=-v:refname"], root)),
    run: async (dir) => {
      try {
        return await execute("go", benchArgs(), { cwd: join(dir, module.dir) });
      } catch (err) {
        // go test reports a failed benchmark on its standard output.
        throw new Error(`${err instanceof Error ? err.message : String(err)}\n${tail(outputOf(err))}`, { cause: err });
      }
    },
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
  const [name, ...rest] = process.argv.slice(2);
  if (name === undefined || rest.length > 0 || !BENCHED.includes(name)) {
    throw new Error(`usage: bench.ts <${BENCHED.join("|")}>`);
  }
  const root = (await git(["rev-parse", "--show-toplevel"])).trim();
  const module = selectModule(root, name);
  const out = mkdtempSync(join(tmpdir(), "limes-bench-"));
  try {
    const c = await compareBenchmarks(bench(root, out, module), root, module);
    info(c.table);
    summarize(report(name, c));
  } finally {
    rmSync(out, { recursive: true, force: true });
  }
}

if (import.meta.main) {
  await runMain(main);
}
