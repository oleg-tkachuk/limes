// When a module is due a release that nobody asked for by hand: the policy
// in .github/release/release-policy.json, and the commits it is applied to.

import { readFileSync } from "node:fs";
import { join } from "node:path";

import { git, lines } from "./git.ts";
import { messageMarksBreaking } from "./markers.ts";
import type { Module, ReleaseCommits } from "./modules.ts";

// POLICY_FILE is the policy, from a checkout's root.
export const POLICY_FILE = ".github/release/release-policy.json";

// Level is the release a commit cuts.
export const Level = { Major: "major", Minor: "minor", Patch: "patch" } as const;
export type Level = (typeof Level)[keyof typeof Level];

// Policy is the release policy: the counts that make a module due, and the
// commit types that make it due at once.
export interface Policy {
  minorCommits: number;
  patchCommits: number;
  immediateTypes: string[];
}

// loadPolicy reads the policy of the checkout at root, refusing one whose
// thresholds are not positive whole numbers.
export function loadPolicy(root: string): Policy {
  const raw = JSON.parse(readFileSync(join(root, POLICY_FILE), "utf8")) as Partial<Policy>;
  for (const key of ["minorCommits", "patchCommits"] as const) {
    const value = raw[key];
    if (typeof value !== "number" || !Number.isInteger(value) || value < 1) {
      throw new Error(`${POLICY_FILE}: ${key} is not a positive whole number`);
    }
  }
  if (!Array.isArray(raw.immediateTypes) || !raw.immediateTypes.every((t) => typeof t === "string")) {
    throw new Error(`${POLICY_FILE}: immediateTypes is not a list of commit types`);
  }
  return { minorCommits: raw.minorCommits as number, patchCommits: raw.patchCommits as number, immediateTypes: raw.immediateTypes };
}

// Pending is one commit since the module's last release that its rules
// release.
export interface Pending {
  hash: string;
  subject: string;
  type: string;
  breaking: boolean;
  level: Level;
}

// SUBJECT_TYPE is a Conventional Commits subject's type.
const SUBJECT_TYPE = /^([A-Za-z0-9_]+)(?:\(.*\))?!?: /;

// typeOf is a subject's commit type, lower case, or empty for none.
export function typeOf(subject: string): string {
  return SUBJECT_TYPE.exec(subject)?.[1]?.toLowerCase() ?? "";
}

// isStableTag reports whether a release tag names no prerelease.
export function isStableTag(tag: string, prefix: string): boolean {
  return !tag.slice(prefix.length).includes("-");
}

// lastStableTag is the module's newest release tag reachable from rev that
// is no prerelease, if any.
export async function lastStableTag(module: Module, rev: string, cwd: string): Promise<string | undefined> {
  const tags = lines(await git(["tag", "--merged", rev, "--list", `${module.prefix}v[0-9]*`, "--sort=-v:refname"], cwd));
  return tags.find((t) => isStableTag(t, module.prefix));
}

// pendingCommits is every commit since the module's last stable release, up
// to rev, that touches the module and that its rules release, with the level
// each cuts. A merge commit is not counted: the commits it brings in are,
// and counting it as well would count their change twice.
export async function pendingCommits(module: Module, rev: string, commits: ReleaseCommits, cwd: string): Promise<Pending[]> {
  const from = await lastStableTag(module, rev, cwd);
  const range = from === undefined ? rev : `${from}..${rev}`;
  const hashes = lines(await git(["log", "--no-merges", "--format=%H", range], cwd));
  const pending: Pending[] = [];
  const scope = module.table?.configFor(module.name);
  for (const hash of hashes) {
    if (scope !== undefined && !commits.touches(commits.changedPaths(cwd, hash), scope.scope)) {
      continue;
    }
    const message = await git(["log", "-1", "--format=%B", hash], cwd);
    const level = scope === undefined ? undefined : await commits.releaseType(scope.analyzer, hash, message);
    if (level === null || level === undefined) {
      continue;
    }
    const subject = message.split("\n")[0] ?? "";
    pending.push({ hash, subject, type: typeOf(subject), breaking: messageMarksBreaking(message), level: level as Level });
  }
  return pending;
}

// Verdict is whether a module is due, and why.
export interface Due {
  due: boolean;
  reason: string;
}

// isDue applies the policy to the pending commits: due at once for a
// breaking change or an immediate type, else once the commits cutting a
// minor, or those cutting a patch, reach their threshold. A commit cutting a
// major is breaking.
export function isDue(pending: readonly Pending[], policy: Policy): Due {
  const urgent = pending.find((p) => p.breaking || p.level === Level.Major || policy.immediateTypes.includes(p.type));
  if (urgent !== undefined) {
    return { due: true, reason: `"${urgent.subject}" is released at once` };
  }
  const minors = pending.filter((p) => p.level === Level.Minor).length;
  const patches = pending.filter((p) => p.level === Level.Patch).length;
  const counts = `${minors} of ${policy.minorCommits} minor, ${patches} of ${policy.patchCommits} patch commits`;
  if (minors >= policy.minorCommits || patches >= policy.patchCommits) {
    return { due: true, reason: `${counts} pending` };
  }
  return { due: false, reason: `${counts} pending; not due yet` };
}
