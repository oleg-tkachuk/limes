// The Go modules the repository releases, as the release scopes them in
// .github/release/modules.cjs: the commits that touch a module, the tags of
// its own release line, the rules it releases by. A checkout without that
// table is one module, the root, which every commit touches.

import { copyFileSync, existsSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { git, lines } from "./git.ts";

// MODULES_CONFIG is the module table, from a checkout's root.
export const MODULES_CONFIG = ".github/release/modules.cjs";
// ROOT_MODULE is the root module's name in it.
export const ROOT_MODULE = "limes";
// RELEASE_CONFIG held the root module's rules before the table did.
const RELEASE_CONFIG = ".github/release/release.config.cjs";
// PRE_STABLE_RULE is the rule that makes a break a minor before v1.0.0.
const PRE_STABLE_RULE = "BREAKING_IS_A_MINOR_BEFORE_1_0";
// STABLE_MAJOR is the first major whose release line keeps its API: from it
// on, a break would tag a major the Go proxy ignores without a new module
// path.
const STABLE_MAJOR = 1;

const require = createRequire(import.meta.url);

// Scope is what the release passes the module-commits plugin.
export interface Scope {
  dir: string;
  exclude: string[];
}

// ModuleTable is what modules.cjs exports, as far as the checks read it.
export interface ModuleTable {
  MODULES: Record<string, { dir: string }>;
  configFor(name: string): { scope: Scope; analyzer: unknown };
  tagPrefix(dir: string): string;
  breaksAreMinor(name: string): boolean;
}

// ReleaseCommits is the release's own filter of the commits that touch a
// module, .github/release/module-commits.cjs, used as it is so the two can
// never count differently.
export interface ReleaseCommits {
  changedPaths(cwd: string, hash: string): string[];
  touches(paths: string[], scope: Scope): boolean;
  releases(analyzer: unknown, hash: string, message: string): Promise<boolean>;
}

// releaseCommits loads the release's filter beside these scripts.
export function releaseCommits(): ReleaseCommits {
  return require("../../release/module-commits.cjs") as ReleaseCommits;
}

// Module is one module the checks answer for.
export interface Module {
  name: string;
  // dir is its directory from the checkout's root, empty for the root module.
  dir: string;
  // prefix starts each of its release tags.
  prefix: string;
  // table is the checkout's module table, undefined for a checkout without.
  table: ModuleTable | undefined;
}

// loadTable loads the module table at path. A table requires nothing, so a
// copy of it loads alone; each path is loaded afresh.
function loadTable(path: string): ModuleTable {
  const copyDir = mkdtempSync(join(tmpdir(), "limes-modules-"));
  try {
    const copy = join(copyDir, "modules.cjs");
    copyFileSync(path, copy);
    return require(copy) as ModuleTable;
  } finally {
    rmSync(copyDir, { recursive: true, force: true });
  }
}

// checkoutTable is the module table of the checkout at root, if it has one.
export function checkoutTable(root: string): ModuleTable | undefined {
  const path = join(root, MODULES_CONFIG);
  return existsSync(path) ? loadTable(path) : undefined;
}

// selectModule is the module name of the checkout at root. A module the
// table does not hold, or any but the root without a table, is an error.
export function selectModule(root: string, name: string = ROOT_MODULE): Module {
  const table = checkoutTable(root);
  if (table === undefined) {
    if (name !== ROOT_MODULE) {
      throw new Error(`no ${MODULES_CONFIG}, so no module ${name}`);
    }
    return { name, dir: "", prefix: "", table };
  }
  const { dir } = table.configFor(name).scope;
  return { name, dir, prefix: table.tagPrefix(dir), table };
}

// releasedModules is every module the checkout at root releases: the root
// module alone without a table.
export function releasedModules(root: string): string[] {
  const table = checkoutTable(root);
  return table === undefined ? [ROOT_MODULE] : Object.keys(table.MODULES);
}

// releaseLine is the module's newest release tag reachable from rev, or
// undefined before any.
export async function releaseLine(module: Module, rev: string, cwd?: string): Promise<string | undefined> {
  return lines(await git(["tag", "--merged", rev, "--list", `${module.prefix}v[0-9]*`, "--sort=-v:refname"], cwd))[0];
}

// onStableLine reports whether the release line tag keeps its API.
export function onStableLine(module: Module, tag: string | undefined): boolean {
  if (tag === undefined) {
    return false;
  }
  const major = Number(tag.slice(module.prefix.length).replace(/^v/, "").split(".")[0]);
  return major >= STABLE_MAJOR;
}

// fileAt is a file's content at rev, or undefined when rev has none.
async function fileAt(rev: string, path: string, cwd?: string): Promise<string | undefined> {
  try {
    return await git(["show", `${rev}:${path}`], cwd);
  } catch {
    return undefined;
  }
}

// releasesMajors reports whether the module, as the release config at rev
// holds it, releases a break as a major. The change that cuts a module's
// v1.0.0 removes the pre-1.0 rule from its rules; the tag follows only once
// the release has run, so a change built on the commit without the rule is
// on the stable line already. A revision with the table is asked through it;
// a table that fails to load is read as releasing majors, so the checks
// refuse a marker, loudly, rather than let one through on a guess. A
// revision from before the table holds the rule in the release config, for
// the root module only; one with neither says nothing either way.
export async function releasesMajors(module: Module, rev: string, cwd?: string): Promise<boolean> {
  const table = await fileAt(rev, MODULES_CONFIG, cwd);
  if (table !== undefined) {
    const dir = mkdtempSync(join(tmpdir(), "limes-modules-"));
    try {
      const path = join(dir, "modules.cjs");
      writeFileSync(path, table);
      const loaded = require(path) as ModuleTable;
      if (loaded.MODULES[module.name] === undefined) {
        return false;
      }
      return !loaded.breaksAreMinor(module.name);
    } catch {
      return true;
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  }
  if (module.name !== ROOT_MODULE) {
    return false;
  }
  const config = await fileAt(rev, RELEASE_CONFIG, cwd);
  return config !== undefined && !config.includes(PRE_STABLE_RULE);
}

// stableAt reports whether a change built on rev must keep the module's API:
// a stable release line is tagged at or below it, or its release config
// already releases breaks as majors.
export async function stableAt(module: Module, rev: string, cwd?: string): Promise<boolean> {
  return onStableLine(module, await releaseLine(module, rev, cwd)) || (await releasesMajors(module, rev, cwd));
}
