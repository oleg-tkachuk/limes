// The checks that keep a change's breaking changes declared, and none on a
// stable release line: the range each module is checked over, its breaking
// markers, and its Go API.

import { mkdtempSync, rmSync } from "node:fs";
import { access } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { execute } from "./lib/exec.ts";
import { git, lines } from "./lib/git.ts";
import { marksBreaking, titleMarksBreaking } from "./lib/markers.ts";
import { releaseLine, stableAt, type Module, type ReleaseCommits } from "./lib/modules.ts";

// PULL_REQUEST_EVENT is the event a pull request's run reports.
export const PULL_REQUEST_EVENT = "pull_request";
// MIGRATION_GUIDE is where each break before v1.0.0 is described as what it
// was and what it is. One guide covers every module.
export const MIGRATION_GUIDE = "docs/migration.md";

// Verdict is a check's answer for one module, and why.
export interface Verdict {
  ok: boolean;
  message: string;
}

// Range is the commits a check reads; base is undefined before any release
// of the module: nothing to check from.
export interface Range {
  base: string | undefined;
  head: string;
}

// checkRange is the commits the marker and API checks read for the module. A
// pull request is checked from its base. A push to main is checked from the
// module's newest release tag below it: a push that reached main past the
// pull request rule — an admin's — carries commits no pull request check
// read, and they must not reach a release unchecked.
export async function checkRange(module: Module, event: string, prBase: string, prHead: string, sha: string, cwd: string): Promise<Range> {
  if (event === PULL_REQUEST_EVENT) {
    return { base: prBase, head: prHead };
  }
  return { base: await releaseLine(module, sha, cwd), head: sha };
}

async function mergeBase(base: string, head: string, cwd: string): Promise<string> {
  return (await git(["merge-base", base, head], cwd)).trim();
}

// changesFile reports whether path differs between from and to.
async function changesFile(from: string, to: string, path: string, cwd: string): Promise<boolean> {
  return lines(await git(["diff", "--name-only", from, to, "--", path], cwd)).length > 0;
}

// markerCheck decides whether a change marks itself breaking where it may.
// From v1.0.0 on — once a v1 or later tag is reachable from the merge base of
// base and head — no commit may: semantic-release would cut a major, which
// the Go proxy ignores without a /vN module path, and every release after it
// would be refused (major-guard.cjs). Before v1.0.0 a marker cuts a minor,
// and passes when the change also edits the migration guide: a break no entry
// describes leaves every consumer to rebuild it from the diff. A title that
// reads as a footer in the merge commit's body marks a break no commit of the
// change carries: refused on any line, as the commits' markers are what a
// break is declared with.
export async function markerCheck(
  module: Module,
  base: string,
  head: string,
  title: string | undefined,
  commits: ReleaseCommits,
  cwd: string,
): Promise<Verdict> {
  const name = module.name;
  if (title !== undefined && titleMarksBreaking(title)) {
    return { ok: false, message: `${name}: the title reads as a BREAKING CHANGE footer in the merge commit; mark the commit instead` };
  }
  const from = await mergeBase(base, head, cwd);
  const line = (await releaseLine(module, from, cwd)) ?? "no release";
  if (!(await stableAt(module, from, cwd))) {
    if ((await marksBreaking(module, from, head, commits, cwd)) && !(await changesFile(from, head, MIGRATION_GUIDE, cwd))) {
      return { ok: false, message: `${name}: a commit marks a break, and ${MIGRATION_GUIDE} has no entry for it` };
    }
    return { ok: true, message: `${name}: no stable release line yet (${line}); breaking markers cut a minor` };
  }
  if (await marksBreaking(module, from, head, commits, cwd)) {
    return {
      ok: false,
      message: `${name}: a commit marks a break on a stable line (${line}), which would tag a major the Go proxy ignores without a module path move`,
    };
  }
  return { ok: true, message: `${name}: no breaking marker on the stable line (${line})` };
}

// apidiffCommand is the apidiff to run: APIDIFF, a command line of its own
// split on whitespace, or the version tools/go.mod pins.
export function apidiffCommand(root: string, override: string | undefined): string[] {
  return override === undefined ? ["go", "tool", `-modfile=${join(root, "tools/go.mod")}`, "apidiff"] : override.trim().split(/\s+/);
}

async function run(command: readonly string[], args: readonly string[], cwd: string): Promise<string> {
  const [file, ...rest] = command;
  if (file === undefined) {
    throw new Error("an empty command");
  }
  return execute(file, [...rest, ...args], { cwd });
}

async function exists(path: string): Promise<boolean> {
  return access(path).then(
    () => true,
    () => false,
  );
}

// commandsOf is the import path of each main package of the module in tree:
// a command exports nothing a consumer can import.
async function commandsOf(tree: string): Promise<string[]> {
  return lines(await execute("go", ["list", "-f", "{{if eq .Name \"main\"}}{{.ImportPath}}{{end}}", "./..."], { cwd: tree }));
}

// withoutCommands drops what apidiff reports of a command: a removed package
// by its import path, a member of a package below the module's root by the
// package's path from that root, matched up to the separator after the name.
export function withoutCommands(breaks: readonly string[], modulePath: string, commands: readonly string[]): string[] {
  const markers = commands.flatMap((c) => [`- package ${c}: `, `- ./${c.startsWith(`${modulePath}/`) ? c.slice(modulePath.length + 1) : c}.`]);
  return breaks.filter((line) => !markers.some((m) => line.includes(m)));
}

// apiBreakCheck compares the module's exported API at the merge base of base
// and head with the one at head. An incompatible change passes only when a
// commit marks itself breaking, since that marker is what makes
// semantic-release cut the version a consumer needs to notice; a commit
// marked breaking with no API change passes, as the wire format and
// behaviour can break without the API doing so. From v1.0.0 on no break
// passes, marked or not: it needs a /vN module path, planned on its own. A
// module not yet at the merge base has no API to break.
export async function apiBreakCheck(
  module: Module,
  base: string,
  head: string,
  apidiff: readonly string[],
  commits: ReleaseCommits,
  cwd: string,
): Promise<Verdict> {
  const name = module.name;
  const dir = module.dir === "" ? "." : module.dir;
  const from = await mergeBase(base, head, cwd);
  const work = mkdtempSync(join(tmpdir(), "limes-api-"));
  const trees = { base: join(work, "base"), head: join(work, "head") };
  try {
    await git(["worktree", "add", "--quiet", "--detach", trees.base, from], cwd);
    await git(["worktree", "add", "--quiet", "--detach", trees.head, head], cwd);
    for (const [which, tree] of Object.entries(trees)) {
      if (!(await exists(join(tree, dir, "go.mod")))) {
        return { ok: true, message: `${name}: no module at the ${which}, so no API to compare` };
      }
    }
    // The path the change leaves the module at, which its API is read under.
    const modulePath = (JSON.parse(await execute("go", ["mod", "edit", "-json"], { cwd: join(trees.head, dir) })) as { Module: { Path: string } }).Module.Path;
    const api = join(work, "base.api");
    await run(apidiff, ["-m", "-w", api, modulePath], join(trees.base, dir));
    const reported = lines(await run(apidiff, ["-m", "-incompatible", api, modulePath], join(trees.head, dir)));
    const commands = [...new Set([...(await commandsOf(join(trees.base, dir))), ...(await commandsOf(join(trees.head, dir)))])];
    const breaks = withoutCommands(reported, modulePath, commands);
    if (breaks.length === 0) {
      return { ok: true, message: `${name}: no incompatible API change` };
    }
    const listing = `Incompatible API changes since ${from}:\n${breaks.join("\n")}\n`;
    if (await stableAt(module, from, cwd)) {
      const line = (await releaseLine(module, from, cwd)) ?? "untagged yet";
      return { ok: false, message: `${listing}${name}: the API is stable on this line (${line}); an incompatible change needs a new module path` };
    }
    if (await marksBreaking(module, from, head, commits, cwd)) {
      return { ok: true, message: `${listing}${name}: a commit marks the change breaking` };
    }
    return { ok: false, message: `${listing}${name}: no commit marks these changes breaking — add ! to the subject or a BREAKING CHANGE: footer` };
  } finally {
    for (const tree of Object.values(trees)) {
      await git(["worktree", "remove", "--force", tree], cwd).catch(() => undefined);
    }
    rmSync(work, { recursive: true, force: true });
  }
}
