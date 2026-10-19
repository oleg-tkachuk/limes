// A throwaway git repository for tests.

import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";

import { git } from "./git.ts";

// AUTHOR signs the test commits; nothing outside the repository sees it.
const AUTHOR = ["-c", "user.name=test", "-c", "user.email=test@example.invalid"];

// TempRepo is a repository in a directory of its own, removed by cleanup.
export interface TempRepo {
  dir: string;
  write(path: string, content: string): void;
  run(...args: string[]): Promise<string>;
  commit(message: string): Promise<string>;
  cleanup(): void;
}

// tempRepo makes an empty repository on main.
export async function tempRepo(): Promise<TempRepo> {
  const dir = mkdtempSync(join(tmpdir(), "limes-ci-"));
  await git(["init", "--quiet", "--initial-branch=main", dir]);
  const repo: TempRepo = {
    dir,
    write(path, content) {
      mkdirSync(dirname(join(dir, path)), { recursive: true });
      writeFileSync(join(dir, path), content);
    },
    run: (...args) => git(args, dir),
    async commit(message) {
      await git(["add", "--all"], dir);
      await git([...AUTHOR, "commit", "--quiet", "--allow-empty", "-m", message], dir);
      return (await git(["rev-parse", "HEAD"], dir)).trim();
    },
    cleanup: () => rmSync(dir, { recursive: true, force: true }),
  };
  return repo;
}
