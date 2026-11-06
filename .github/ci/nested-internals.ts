// Build each nested module's releases against limes as it is.
//
// Usage: node nested-internals.ts
//
// A nested module imports some of limes's internal packages — internal/errs,
// internal/errcode, internal/jose — and its released versions keep doing so
// when a consumer pairs them with a later limes: an exchange v1.0 beside a
// limes v1.N. Go lets the import through, the internal path being under
// the same module path, so nothing but a build says whether that pair still
// compiles. For each module in .github/release/modules.cjs but the root,
// this checks its newest release out — and, once the module is at v1 or
// later, every release of its current major —, points its requirement of
// limes at this checkout, and runs go build and go vet over its production
// sources. A release that no longer builds fails the run, naming the module,
// the tag and the internal symbols the compiler named; each module's verdict
// goes on the run's summary page.

import { mkdtempSync, readdirSync, readFileSync, rmSync, statSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { code, error, info, summarize, table } from "./lib/actions.ts";
import { execute } from "./lib/exec.ts";
import { git, lines } from "./lib/git.ts";
import { runMain } from "./lib/main.ts";
import { ROOT_MODULE, releasedModules, selectModule, type Module } from "./lib/modules.ts";

// LIMES_MODULE is the root module's path, which each nested module
// requires and whose internal packages it may import.
export const LIMES_MODULE = "github.com/oleg-tkachuk/limes";
// INTERNAL_IMPORT is an import of one of limes's internal packages, in a
// Go source: its path.
const INTERNAL_IMPORT = new RegExp(`"(${LIMES_MODULE.replaceAll(".", "\\.")}/internal/[a-z0-9_/]+)"`, "g");
// STABLE_MAJOR is the first major whose releases keep their API: from it
// on, every release of the module's current major is checked, as each is a
// version a consumer may still pair with this limes.
export const STABLE_MAJOR = 1;
// GO_SOURCE and TEST_SOURCE name a Go source and a test's.
const GO_SOURCE = ".go";
const TEST_SOURCE = "_test.go";
// GO_MOD names a module's root.
const GO_MOD = "go.mod";
// The Go commands each release is held to, in its module's directory.
export const GO_CHECKS: readonly (readonly string[])[] = [
  ["build", "./..."],
  ["vet", "./..."],
];
// GO_ENV is how the checks run Go: outside any workspace, writing what
// go.sum lacks for the limes the module is pointed at, which may require
// more than the release's own go.sum holds.
const GO_ENV = { GOWORK: "off", GOFLAGS: "-mod=mod" };

// Version is a release tag's version, parsed.
export interface Version {
  tag: string;
  major: number;
  minor: number;
  patch: number;
}

// RELEASE_VERSION is a release's version after its prefix: vMAJOR.MINOR.PATCH,
// no prerelease, no build.
const RELEASE_VERSION = /^v(\d+)\.(\d+)\.(\d+)$/;

// versionOf is tag's version under prefix, undefined for a prerelease or a
// tag of another shape.
export function versionOf(prefix: string, tag: string): Version | undefined {
  if (!tag.startsWith(prefix)) {
    return undefined;
  }
  const m = RELEASE_VERSION.exec(tag.slice(prefix.length));
  if (m === null) {
    return undefined;
  }
  return { tag, major: Number(m[1]), minor: Number(m[2]), patch: Number(m[3]) };
}

// newestFirst orders versions from the newest.
function newestFirst(a: Version, b: Version): number {
  return b.major - a.major || b.minor - a.minor || b.patch - a.patch;
}

// releasesToCheck are the tags of a module's releases to build, newest
// first: the newest release alone before STABLE_MAJOR, and from it on every
// release of the newest's major. None before any release.
export function releasesToCheck(prefix: string, tags: readonly string[]): string[] {
  const versions = tags.flatMap((t) => versionOf(prefix, t) ?? []).sort(newestFirst);
  const newest = versions[0];
  if (newest === undefined) {
    return [];
  }
  if (newest.major < STABLE_MAJOR) {
    return [newest.tag];
  }
  return versions.filter((v) => v.major === newest.major).map((v) => v.tag);
}

// internalImports are the paths of limes's internal packages source
// imports, each once, in the order first named.
export function internalImports(source: string): string[] {
  const out: string[] = [];
  for (const [, path] of source.matchAll(INTERNAL_IMPORT)) {
    if (path !== undefined && !out.includes(path)) {
      out.push(path);
    }
  }
  return out;
}

// packageName is the name an import path is referred to by: its last
// element.
function packageName(path: string): string {
  return path.slice(path.lastIndexOf("/") + 1);
}

// brokenSymbols are what output, a failed build's, names of the internal
// packages imports lists: each package.Symbol it names, and each package it
// names by path — one removed, say —, sorted, each once.
export function brokenSymbols(output: string, imports: readonly string[]): string[] {
  const found = new Set<string>();
  for (const path of imports) {
    if (output.includes(path)) {
      found.add(path);
    }
    const name = packageName(path);
    for (const [symbol] of output.matchAll(new RegExp(`\\b${name}\\.[A-Za-z_][A-Za-z0-9_]*`, "g"))) {
      found.add(symbol);
    }
  }
  return [...found].sort();
}

// goSources are the .go files under dir, a module's, leaving out any
// module nested below it: go build ./... does not reach one.
function goSources(dir: string, top = true): string[] {
  if (!top && readdirSync(dir).includes(GO_MOD)) {
    return [];
  }
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) {
      return goSources(path, false);
    }
    return name.endsWith(GO_SOURCE) ? [path] : [];
  });
}

// Verdict is what the check found of one release.
export interface Verdict {
  ok: boolean;
  message: string;
}

// failure is the verdict on module's release tag that go, run with args,
// refused with output.
export function failure(module: string, tag: string, args: readonly string[], output: string, imports: readonly string[]): Verdict {
  const symbols = brokenSymbols(output, imports);
  const named = symbols.length > 0 ? `the internal symbols it uses: ${symbols.join(", ")}` : "no internal symbol named; see the output";
  return {
    ok: false,
    message: `${module}: ${tag} no longer passes go ${args.join(" ")} against this limes — ${named}\n${output.trim()}`,
  };
}

// checkRelease builds module's release tag, its production sources, against
// the limes checked out at root.
export async function checkRelease(root: string, module: Module, tag: string): Promise<Verdict> {
  const work = mkdtempSync(join(tmpdir(), "limes-nested-"));
  const tree = join(work, "tree");
  try {
    await git(["worktree", "add", "--quiet", "--detach", tree, tag], root);
    const dir = join(tree, module.dir);
    const sources = goSources(dir);
    // A consumer builds a release's production code, never its tests,
    // which may use internal helpers no release promises to keep.
    for (const path of sources.filter((p) => p.endsWith(TEST_SOURCE))) {
      rmSync(path, { force: true });
    }
    const imports = [...new Set(sources.filter((p) => !p.endsWith(TEST_SOURCE)).flatMap((p) => internalImports(readFileSync(p, "utf8"))))];
    const env = { ...process.env, ...GO_ENV };
    await execute("go", ["mod", "edit", `-replace=${LIMES_MODULE}=${root}`], { cwd: dir, env });
    for (const args of GO_CHECKS) {
      try {
        await execute("go", args, { cwd: dir, env });
      } catch (err) {
        return failure(module.name, tag, args, err instanceof Error ? err.message : String(err), imports);
      }
    }
    return { ok: true, message: `${module.name}: ${tag} builds against this limes (internal packages: ${imports.join(", ") || "none"})` };
  } finally {
    await git(["worktree", "remove", "--force", tree], root).catch(() => undefined);
    rmSync(work, { recursive: true, force: true });
  }
}

// Outcome is what the check found of one module.
export const Outcome = { Passed: "passed", Failed: "failed", Skipped: "skipped" } as const;
export type Outcome = (typeof Outcome)[keyof typeof Outcome];

// ModuleResult is one module's outcome, with what the check said of it.
export interface ModuleResult {
  module: string;
  outcome: Outcome;
  message: string;
}

// NO_RELEASE is what a module never released is skipped with.
const NO_RELEASE = "no release to build yet";

// ReleaseCheck checks one release of a module.
export type ReleaseCheck = (module: Module, tag: string) => Promise<Verdict>;

// checkNested runs check over the releases to check of every nested module
// of the checkout at root, in the table's order, and says what it found of
// each module.
export async function checkNested(root: string, check: ReleaseCheck): Promise<ModuleResult[]> {
  const results: ModuleResult[] = [];
  for (const name of releasedModules(root).filter((n) => n !== ROOT_MODULE)) {
    const module = selectModule(root, name);
    const tags = releasesToCheck(module.prefix, lines(await git(["tag", "--list", `${module.prefix}v[0-9]*`], root)));
    if (tags.length === 0) {
      info(`${name}: ${NO_RELEASE}`);
      results.push({ module: name, outcome: Outcome.Skipped, message: NO_RELEASE });
      continue;
    }
    const failed: string[] = [];
    for (const tag of tags) {
      const verdict = await check(module, tag);
      if (verdict.ok) {
        info(verdict.message);
      } else {
        error(verdict.message);
        failed.push(verdict.message);
      }
    }
    results.push(
      failed.length === 0
        ? { module: name, outcome: Outcome.Passed, message: `${tags.join(", ")} build against this limes` }
        : { module: name, outcome: Outcome.Failed, message: failed.join("\n") },
    );
  }
  return results;
}

// allPassed reports whether no module failed: one skipped had nothing to
// build.
export function allPassed(results: readonly ModuleResult[]): boolean {
  return results.every((r) => r.outcome !== Outcome.Failed);
}

// SUMMARY_TITLE heads the summary page section, as the job is named;
// SUMMARY_HEADER heads the table under it.
const SUMMARY_TITLE = "Nested releases against limes";
const SUMMARY_HEADER = ["Module", "Result", "Detail"];

// nestedSummary is the summary page section of results.
export function nestedSummary(results: readonly ModuleResult[]): string {
  // A message quotes the compiler's output: shown as code, never as markup.
  return `### ${SUMMARY_TITLE}\n\n${table(SUMMARY_HEADER, results.map((r) => [r.module, r.outcome, code(r.message)]))}`;
}

async function main(): Promise<boolean> {
  const root = (await git(["rev-parse", "--show-toplevel"])).trim();
  const results = await checkNested(root, (module, tag) => checkRelease(root, module, tag));
  summarize(nestedSummary(results));
  return allPassed(results);
}

if (import.meta.main) {
  await runMain(main);
}
