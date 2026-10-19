// git, run as an argument vector: no shell ever sees a ref or a path.

import { execute } from "./exec.ts";

// git runs git with args in cwd and returns its standard output.
export async function git(args: readonly string[], cwd?: string): Promise<string> {
  return execute("git", args, { cwd });
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
