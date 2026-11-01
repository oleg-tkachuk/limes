// List every fuzz target of every module the repository releases, for
// fuzz.yaml's matrix.
//
// Usage: node fuzz-targets.ts (in the checkout's root)
//
// Each target goes with its module's directory, where go must run, and its
// package, as -fuzz takes one package at a time. The modules are those of
// the module table (.github/release/modules.cjs), so a nested module's
// targets are fuzzed too, and a new module's without an edit here. A
// package is asked for its targets by `go test -list` only when one of its
// test files declares one: listing runs the package's TestMain, and
// pgstore's starts a PostgreSQL.

import { readFileSync } from "node:fs";
import { join } from "node:path";

import { info, setOutput } from "./lib/actions.ts";
import { execute } from "./lib/exec.ts";
import { goEnv } from "./lib/go-modules.ts";
import { runMain } from "./lib/main.ts";
import { releasedModules, selectModule } from "./lib/modules.ts";

// TARGETS_OUTPUT is the step output fuzz.yaml's matrix reads.
export const TARGETS_OUTPUT = "targets";
// ROOT_DIR is the root module's directory as a working directory.
export const ROOT_DIR = ".";
// LISTED_FIELDS are what go list is asked of each package.
const LISTED_FIELDS = "ImportPath,Dir,TestGoFiles,XTestGoFiles";
// FUZZ_PREFIX begins every fuzz target's name; FUZZ_DECLARATION is a test
// file declaring one, which decides only whether go is asked.
const FUZZ_PREFIX = "Fuzz";
const FUZZ_DECLARATION = /^func Fuzz\w*\(/m;
// OBJECT_BOUNDARY is where go list -json ends one package and begins the
// next: each object opens at the start of a line.
const OBJECT_BOUNDARY = /\n(?=\{)/;

// FuzzTarget is one entry of the matrix.
export interface FuzzTarget {
  module: string;
  package: string;
  target: string;
}

// ListedPackage is a package as go list -json writes the fields asked.
export interface ListedPackage {
  ImportPath: string;
  Dir: string;
  TestGoFiles?: string[];
  XTestGoFiles?: string[];
}

// Go runs go with args in cwd and returns its standard output.
export type Go = (args: readonly string[], cwd: string) => Promise<string>;
// ReadFile reads a file's text.
export type ReadFile = (path: string) => string;

// parsePackages reads the packages of go list -json's output.
export function parsePackages(out: string): ListedPackage[] {
  const text = out.trim();
  return text === "" ? [] : text.split(OBJECT_BOUNDARY).map((object) => JSON.parse(object) as ListedPackage);
}

// declaresFuzz reports whether one of pkg's test files declares a fuzz target.
export function declaresFuzz(pkg: ListedPackage, read: ReadFile): boolean {
  const files = [...(pkg.TestGoFiles ?? []), ...(pkg.XTestGoFiles ?? [])];
  return files.some((file) => FUZZ_DECLARATION.test(read(join(pkg.Dir, file))));
}

// targetNames are the fuzz targets in go test -list's output, which ends
// with the package's "ok" line.
export function targetNames(out: string): string[] {
  return out.split("\n").filter((line) => line.startsWith(FUZZ_PREFIX));
}

// fuzzTargets lists the fuzz targets of every module released from the
// checkout at root, module by module in the table's order.
export async function fuzzTargets(root: string, go: Go, read: ReadFile): Promise<FuzzTarget[]> {
  const targets: FuzzTarget[] = [];
  for (const name of releasedModules(root)) {
    const { dir } = selectModule(root, name);
    const module = dir === "" ? ROOT_DIR : dir;
    const cwd = join(root, module);
    for (const pkg of parsePackages(await go(["list", `-json=${LISTED_FIELDS}`, "./..."], cwd))) {
      if (!declaresFuzz(pkg, read)) {
        continue;
      }
      for (const target of targetNames(await go(["test", "-list", `^${FUZZ_PREFIX}`, pkg.ImportPath], cwd))) {
        targets.push({ module, package: pkg.ImportPath, target });
      }
    }
  }
  return targets;
}

async function main(): Promise<void> {
  const go: Go = (args, cwd) => execute("go", args, { cwd, env: goEnv() });
  const targets = await fuzzTargets(process.cwd(), go, (path) => readFileSync(path, "utf8"));
  if (targets.length === 0) {
    throw new Error("no fuzz targets found");
  }
  for (const t of targets) {
    info(`${t.module} ${t.package} ${t.target}`);
  }
  setOutput(TARGETS_OUTPUT, JSON.stringify(targets));
}

if (import.meta.main) {
  await runMain(main);
}
