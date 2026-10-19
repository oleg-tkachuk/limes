// go mod tidy every nested module that builds the root module through a
// local replace.
//
// Usage: node tidy-modules.ts
//
// Such a module — pgstore, examples — resolves limes from the checkout, so a
// requirement the root raises is raised in its graph too, and its go.mod is
// untidy until it is tidied. deps:update tidies the root alone; CI then fails
// the nested module's tidy and API checks.
//
// The modules are found from their go.mod, not listed here, so a nested
// module that gains or drops the replace needs no edit of this script. A
// module that also replaces another nested module in (examples replaces
// pgstore) is tidied after it: fewer local replaces first. Only go.mod files
// git tracks count.

import { dirname, join } from "node:path";

import { info } from "./lib/actions.ts";
import { execute } from "./lib/exec.ts";
import { git, lines } from "./lib/git.ts";
import { runMain } from "./lib/main.ts";

// GoMod is what `go mod edit -json` says of a go.mod, as far as it is read.
interface GoMod {
  Module: { Path: string };
  Replace?: { Old: { Path: string }; New: { Path: string; Version?: string } }[];
}

async function goMod(path: string): Promise<GoMod> {
  return JSON.parse(await execute("go", ["mod", "edit", "-json", path])) as GoMod;
}

// replacingModules is the directory of every nested module git tracks that
// replaces the module at root with a directory, fewer local replaces first,
// then by directory.
export async function replacingModules(root: string): Promise<string[]> {
  const rootPath = (await goMod(join(root, "go.mod"))).Module.Path;
  const found: { dir: string; locals: number }[] = [];
  for (const file of lines(await git(["ls-files", "--", "*/go.mod"], root))) {
    // A local replace is one whose target has no version: a directory.
    const locals = ((await goMod(join(root, file))).Replace ?? []).filter((r) => r.New.Version === undefined);
    if (locals.some((r) => r.Old.Path === rootPath)) {
      found.push({ dir: dirname(file), locals: locals.length });
    }
  }
  return found.sort((a, b) => a.locals - b.locals || a.dir.localeCompare(b.dir)).map((m) => m.dir);
}

// tidyReplacingModules tidies each of them in turn, with tidy.
export async function tidyReplacingModules(root: string, tidy: (dir: string) => Promise<void>): Promise<string[]> {
  const dirs = await replacingModules(root);
  for (const dir of dirs) {
    await tidy(dir);
    info(`tidy-modules: ${dir} tidied`);
  }
  return dirs;
}

async function main(): Promise<void> {
  if (process.argv.length > 2) {
    throw new Error("usage: tidy-modules.ts");
  }
  const root = (await git(["rev-parse", "--show-toplevel"])).trim();
  await tidyReplacingModules(root, async (dir) => void (await execute("go", ["-C", join(root, dir), "mod", "tidy"])));
}

if (import.meta.main) {
  await runMain(main);
}
