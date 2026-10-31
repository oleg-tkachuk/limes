// The Go modules of the checkout as go reads them: which limes each
// requires, and raising it to a release.

import { existsSync } from "node:fs";
import { join } from "node:path";

import { info } from "./actions.ts";
import { execute } from "./exec.ts";

// LIMES_MODULE is the module every nested module requires.
export const LIMES_MODULE = "github.com/oleg-tkachuk/limes";
// FOLLOWERS are the modules never released that build against the nested
// ones through a replace. Raising a nested module's requirement of limes
// raises theirs, so their go.mod, tidied, follows in the same commit: left
// behind, their own check that go.mod is tidy fails the pull request.
export const FOLLOWERS: readonly string[] = ["examples"];
// goEnv is the environment go runs in: the step's, without the token.
export function goEnv(): NodeJS.ProcessEnv {
  const env = { ...process.env };
  delete env.GH_TOKEN;
  delete env.GITHUB_TOKEN;
  return env;
}

async function go(args: string[], cwd: string): Promise<string> {
  return execute("go", args, { cwd, env: goEnv() });
}

// requiredLimes is the limes version the module in dir requires, if any.
export async function requiredLimes(dir: string): Promise<string | undefined> {
  const mod = JSON.parse(await go(["mod", "edit", "-json"], dir)) as { Require?: { Path: string; Version: string }[] };
  return mod.Require?.find((r) => r.Path === LIMES_MODULE)?.Version;
}

// compareVersions orders two versions, vX.Y.Z with or without a
// prerelease, by SemVer 2.0.0 §11: a prerelease comes before its release,
// and two prereleases compare identifier by identifier — numbers as numbers,
// before any alphanumeric one, which compare in ASCII order — the shorter
// first when one is the other's prefix.
export function compareVersions(a: string, b: string): number {
  const va = parseVersion(a);
  const vb = parseVersion(b);
  const core = va.major - vb.major || va.minor - vb.minor || va.patch - vb.patch;
  if (core !== 0) return core;
  if (va.pre.length === 0 || vb.pre.length === 0) return vb.pre.length - va.pre.length;
  for (const [i, id] of va.pre.entries()) {
    const other = vb.pre[i];
    if (other === undefined) return 1;
    const d = compareIdentifiers(id, other);
    if (d !== 0) return d;
  }
  return va.pre.length - vb.pre.length;
}

// Version is a version's parts that order it; build metadata does not.
interface Version {
  major: number;
  minor: number;
  patch: number;
  pre: string[];
}

// VERSION reads vX.Y.Z, its prerelease and its build metadata.
const VERSION = /^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$/;

// parseVersion is v's parts, or a refusal of what is no version.
function parseVersion(v: string): Version {
  const m = VERSION.exec(v);
  if (m === null) throw new Error(`${v} is not a version`);
  const [, major = "", minor = "", patch = "", pre] = m;
  return { major: Number(major), minor: Number(minor), patch: Number(patch), pre: pre ? pre.split(".") : [] };
}

// NUMERIC is a prerelease identifier of digits only.
const NUMERIC = /^\d+$/;

// compareIdentifiers orders two prerelease identifiers.
function compareIdentifiers(a: string, b: string): number {
  const [na, nb] = [NUMERIC.test(a), NUMERIC.test(b)];
  if (na && nb) return Number(a) - Number(b);
  if (na !== nb) return na ? -1 : 1;
  return a < b ? -1 : a > b ? 1 : 0;
}

// raise raises the module in dir to tag in the checkout, and says what it
// required, or undefined when it is left alone.
export async function raise(root: string, dir: string, tag: string): Promise<string | undefined> {
  const moduleDir = join(root, dir);
  const current = await requiredLimes(moduleDir);
  if (current === undefined) {
    info(`${dir} does not require ${LIMES_MODULE}`);
    return undefined;
  }
  if (compareVersions(current, tag) >= 0) {
    info(`${dir} already requires limes ${current}`);
    return undefined;
  }
  await go(["mod", "edit", `-require=${LIMES_MODULE}@${tag}`], moduleDir);
  await go(["mod", "tidy"], moduleDir);
  return current;
}

// tidyFollowers tidies each follower that is a module under root, and is
// the files it may have changed.
export async function tidyFollowers(root: string): Promise<string[]> {
  const changed: string[] = [];
  for (const follower of FOLLOWERS) {
    if (existsSync(join(root, follower, "go.mod"))) {
      await go(["mod", "tidy"], join(root, follower));
      changed.push(join(follower, "go.mod"), join(follower, "go.sum"));
    }
  }
  return changed;
}
