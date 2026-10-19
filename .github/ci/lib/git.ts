// git, run as an argument vector: no shell ever sees a ref or a path.

import { execFile } from "node:child_process";
import { promisify } from "node:util";

const run = promisify(execFile);

// MAX_OUTPUT_BYTES bounds what one git call may print: a diff's name list in
// a large change is the most any caller reads.
const MAX_OUTPUT_BYTES = 64 * 1024 * 1024;

// git runs git with args in cwd and returns its standard output.
export async function git(args: readonly string[], cwd?: string): Promise<string> {
  const { stdout } = await run("git", [...args], { cwd, maxBuffer: MAX_OUTPUT_BYTES, encoding: "utf8" });
  return stdout;
}

// lines splits output into its non-empty lines.
export function lines(output: string): string[] {
  return output.split("\n").filter((line) => line !== "");
}

// hasCommit reports whether this clone holds the commit sha names.
export async function hasCommit(sha: string, cwd?: string): Promise<boolean> {
  try {
    await git(["cat-file", "-e", `${sha}^{commit}`], cwd);
    return true;
  } catch {
    return false;
  }
}

// changedPaths lists the paths that differ between from and to. A rename is
// listed by both its paths: moving a file away from its place changes what
// reads it there as surely as deleting it does.
export async function changedPaths(from: string, to: string, cwd?: string): Promise<string[]> {
  return lines(await git(["diff", "--name-only", "--no-renames", from, to], cwd));
}
