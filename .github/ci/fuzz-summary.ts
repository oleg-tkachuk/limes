// Put one fuzz target's run on the run's summary page.
//
// Usage: PACKAGE=… TARGET=… OUTCOME=… node fuzz-summary.ts <log>
//
// fuzz.yaml runs `go test -fuzz` for one target into <log>, then this, whatever
// the run's outcome (the step's, success or failure): how long the target
// was searched, how many inputs it ran and how many widened its coverage —
// read from the last progress line go prints — and, for a failure, the input
// go wrote and the command that runs it again, which go prints too. A target
// that failed before its search began, on its seed corpus, wrote no input,
// and says so.

import { readFileSync } from "node:fs";

import { summarize, table } from "./lib/actions.ts";
import { requireEnv } from "./lib/env.ts";
import { runMain } from "./lib/main.ts";

// PROGRESS is go's line of how a search stands; FAILING_INPUT and RERUN, the
// lines it prints of an input that failed.
const PROGRESS = /^fuzz: elapsed: (\S+), execs: (\d+) \((\d+)\/sec\), new interesting: (\d+) \(total: (\d+)\)$/;
const FAILING_INPUT = /^\s*Failing input written to (\S+)$/;
const RERUN = /^\s*(go test -run=\S+)$/;

// SUCCESS is the outcome of a step that passed.
export const SUCCESS = "success";

// FuzzRun is what a target's log says of its run.
export interface FuzzRun {
  // elapsed, executions and interesting are of the last progress line; none
  // when the search never began.
  elapsed?: string;
  executions?: number;
  interesting?: number;
  // input and rerun are the failing input go wrote, and how to run it again.
  input?: string;
  rerun?: string;
}

// parseFuzzLog reads a target's run from the log of `go test -fuzz`.
export function parseFuzzLog(log: string): FuzzRun {
  const run: FuzzRun = {};
  for (const raw of log.split("\n")) {
    const line = raw.trimEnd();
    const [, elapsed, executions, , interesting] = PROGRESS.exec(line) ?? [];
    if (elapsed !== undefined) {
      run.elapsed = elapsed;
      run.executions = Number(executions);
      run.interesting = Number(interesting);
      continue;
    }
    const [, input] = FAILING_INPUT.exec(line) ?? [];
    if (input !== undefined) {
      run.input = input;
      continue;
    }
    const [, rerun] = RERUN.exec(line) ?? [];
    if (rerun !== undefined) {
      run.rerun = rerun;
    }
  }
  return run;
}

// SUMMARY_HEADER heads the summary table; NONE fills a cell the log had no
// value for.
const SUMMARY_HEADER = ["Package", "Searched", "Inputs run", "New interesting", "Result"];
const NONE = "—";

// fuzzSummary is the summary page section of target in pkg, whose step
// ended in outcome.
export function fuzzSummary(pkg: string, target: string, outcome: string, run: FuzzRun): string {
  const passed = outcome === SUCCESS;
  const row = [pkg, run.elapsed ?? NONE, run.executions?.toString() ?? NONE, run.interesting?.toString() ?? NONE, passed ? "passed" : "failed"];
  const section = `### Fuzz ${target}\n\n${table(SUMMARY_HEADER, [row])}`;
  if (passed) {
    return section;
  }
  if (run.input === undefined) {
    return `${section}\nIt failed before its search wrote an input: on its seed corpus, or before it began. See the log.\n`;
  }
  const rerun = `${run.rerun ?? `go test -run=${target}`} ${pkg}`;
  return `${section}\nThe failing input is \`${run.input}\`, under the package, in this run's artifact \`fuzz-${target}\`; committed there, it stays a regression test. To run it again:\n\n\`\`\`bash\n${rerun}\n\`\`\`\n`;
}

async function main(): Promise<void> {
  const [logFile] = process.argv.slice(2);
  if (logFile === undefined) {
    throw new Error("usage: fuzz-summary.ts <log>");
  }
  const pkg = requireEnv("PACKAGE");
  summarize(fuzzSummary(pkg, requireEnv("TARGET"), requireEnv("OUTCOME"), parseFuzzLog(readFileSync(logFile, "utf8"))));
}

if (import.meta.main) {
  await runMain(main);
}
