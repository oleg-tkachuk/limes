// A release candidate of a module: the version its next release would be,
// tagged -rc.N on the checked-out commit, by hand only.

import { git, lines } from "./git.ts";
import { Level, lastStableTag, type Pending } from "./release-policy.ts";
import type { Module } from "./modules.ts";

// RC is the prerelease identifier of a release candidate.
export const RC = "rc";
// STABLE_MAJOR is the first major whose line keeps its API: a major past it
// needs a new module path (major-guard.cjs), so no candidate is cut for one.
const STABLE_MAJOR = 1;

// LEVEL_ORDER ranks the levels, highest first.
const LEVEL_ORDER: readonly Level[] = [Level.Major, Level.Minor, Level.Patch];

// highestLevel is the highest release any pending commit cuts.
export function highestLevel(pending: readonly Pending[]): Level | undefined {
  return LEVEL_ORDER.find((l) => pending.some((p) => p.level === l));
}

// Version is a release's three numbers.
interface Version {
  major: number;
  minor: number;
  patch: number;
}

// parseVersion reads vX.Y.Z after prefix.
export function parseVersion(tag: string, prefix: string): Version {
  const match = /^v([0-9]+)\.([0-9]+)\.([0-9]+)$/.exec(tag.slice(prefix.length));
  if (match === null) {
    throw new Error(`${tag} is not a release tag of ${prefix || "the root module"}`);
  }
  return { major: Number(match[1]), minor: Number(match[2]), patch: Number(match[3]) };
}

// nextVersion is the version a release at level makes of last.
export function nextVersion(last: Version, level: Level): string {
  switch (level) {
    case Level.Major:
      if (last.major >= STABLE_MAJOR) {
        throw new Error(`a major past v${last.major} needs a new module path; no candidate is cut for it`);
      }
      return `${last.major + 1}.0.0`;
    case Level.Minor:
      return `${last.major}.${last.minor + 1}.0`;
    case Level.Patch:
      return `${last.major}.${last.minor}.${last.patch + 1}`;
  }
}

// nextCandidate is the tag of the next release candidate of version: one past
// the highest -rc.N already tagged for it.
export async function nextCandidate(module: Module, version: string, cwd: string): Promise<string> {
  const base = `${module.prefix}v${version}-${RC}.`;
  const taken = lines(await git(["tag", "--list", `${base}*`], cwd))
    .map((t) => Number(t.slice(base.length)))
    .filter((n) => Number.isInteger(n));
  return `${base}${Math.max(0, ...taken) + 1}`;
}

// candidateTag is the tag a release candidate of the module would take for
// the pending commits, or undefined when none of them releases anything.
// With nextMajor it is a candidate of the next major whatever is pending —
// limes's v1.0.0-rc.N while it is on 0.x — which the module's rules on main,
// a breaking change being a minor before 1.0, would never reach.
export async function candidateTag(module: Module, pending: readonly Pending[], cwd: string, nextMajor = false): Promise<string | undefined> {
  const level = nextMajor ? Level.Major : highestLevel(pending);
  if (level === undefined) {
    return undefined;
  }
  const last = await lastStableTag(module, "HEAD", cwd);
  if (last === undefined) {
    throw new Error(`${module.name} has no release yet to cut a candidate after`);
  }
  return nextCandidate(module, nextVersion(parseVersion(last, module.prefix), level), cwd);
}
