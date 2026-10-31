// Cut a release candidate of a module, by hand: the version its next release
// would be, tagged -rc.N on main's head and published as a GitHub
// prerelease.
//
// Usage: GH_TOKEN=… GITHUB_REPOSITORY=… RELEASE_DEPLOY_KEY=… RUNNER_TEMP=…
//        CHANNEL=prerelease MANUAL=true [MODULE=all|<name>] [NEXT_MAJOR=true] node release.ts
//
// A release is not cut here: release-plan.ts proposes it and release-tag.ts
// tags it (docs/adr/0016-one-release-commit.md). A candidate needs no commit,
// so it is still cut straight from main's head. For each module of
// .github/release/modules.cjs, in its order, a semantic-release dry run gives
// the notes of the release the candidate stands for; the tag is pushed with
// the deploy key, which alone may create a release tag, published as a
// prerelease and the Go proxy told of it. With NEXT_MAJOR it is a candidate
// of limes's next major, v1.0.0-rc.N, set as the step output
// `limes-candidate` once published, whose row in the migration guide is
// then proposed (candidates.ts). One module's failure leaves the others cut,
// and fails the run. Last, every recent tag an earlier run pushed and never
// published is published.

import { existsSync, rmSync } from "node:fs";
import { join } from "node:path";

import { error, info, notice, setOutput, summarize, table } from "./lib/actions.ts";
import { withDeployKey } from "./lib/deploy-key.ts";
import { optionalEnv, requireEnv } from "./lib/env.ts";
import { git } from "./lib/git.ts";
import { gitHubFor, type GitHub } from "./lib/github.ts";
import { runMain } from "./lib/main.ts";
import { ROOT_MODULE, releaseCommits, releasedModules, selectModule, type Module, type ReleaseCommits } from "./lib/modules.ts";
import { candidateTag, previousCandidate, RC, releasesSince, sinceCandidate } from "./lib/prerelease.ts";
import { lastStableTag, pendingCommits } from "./lib/release-policy.ts";
import { notesFrom, publish, publishLeftBehind, warmGoProxy, type Fetch } from "./lib/releases.ts";
import { seedLine } from "./lib/seed.ts";
import { DRY_RUN_ARG, PUSH_URL_ENV, semanticRelease, semanticReleaseCommand } from "./lib/semantic-release.ts";

export { PUSH_URL_ENV, semanticReleaseCommand };

// OUTPUT_LIMES_CANDIDATE is the step output the job proposing the
// candidate's row in the migration guide reads (candidates.ts).
export const OUTPUT_LIMES_CANDIDATE = "limes-candidate";
// Channel is what a run by hand asks for: a release, which release-plan.ts
// proposes, or release candidates, which this cuts.
export const Channel = { Release: "release", Prerelease: "prerelease" } as const;
export type Channel = (typeof Channel)[keyof typeof Channel];
// ALL_MODULES cuts a candidate of every module.
export const ALL_MODULES = "all";

// Release is everything one run needs, its effects injected so a test can
// stand in for semantic-release, GitHub and the Go proxy.
export interface Release {
  github: GitHub;
  repository: string;
  root: string;
  // candidateNotes writes to notesFile the notes module's next release
  // would have, from a dry run that tags nothing, and nothing when no
  // release is due.
  candidateNotes(module: Module, notesFile: string): Promise<void>;
  fetchInfo: Fetch;
  notesDir: string;
  // onMajorCandidate is told the tag of a candidate of the next major, once
  // it is published.
  onMajorCandidate(tag: string): void;
  // only names the one module to cut a candidate of; undefined for every one.
  only: string | undefined;
  // nextMajor: a candidate of limes's next major, v1.0.0-rc.N, rather than
  // of its next version by the pending commits. Between two such candidates
  // the API may still break: it is 0.x on main until the release that cuts
  // 1.0.
  nextMajor: boolean;
  commits: ReleaseCommits;
  // pushTag pushes a tag made in the checkout.
  pushTag(tag: string): Promise<void>;
}

// Outcome is what a run did with one module: the tag it cut, if any, and
// why.
export interface Outcome {
  tag?: string;
  note: string;
}

// sinceTag is the module's last release, as a note names it.
async function sinceTag(module: Module, root: string): Promise<string> {
  return (await lastStableTag(module, "HEAD", root)) ?? "its first commit";
}

// prereleaseModule tags and publishes the module's next release candidate,
// if anything is pending.
export async function prereleaseModule(r: Release, module: Module): Promise<Outcome> {
  const tag = await candidateTag(module, await pendingCommits(module, "HEAD", r.commits, r.root), r.root, r.nextMajor);
  if (tag === undefined) {
    return { note: `no commit since ${await sinceTag(module, r.root)} cuts a release, so no candidate` };
  }
  // Read before the tag is made: the candidate's notes are those of the
  // release it stands for, from the last release on.
  const notesFile = join(r.notesDir, `release-notes-${module.name}-${RC}.md`);
  // A file from an earlier run would read as this dry run's notes.
  rmSync(notesFile, { force: true });
  await r.candidateNotes(module, notesFile);
  // No notes: semantic-release found nothing to release. For a candidate
  // of the next major that is the last release's code as it is, cut as the
  // 1.0 it stands for. For any other candidate the policy found one due and
  // the two disagree on what is pending: nothing is tagged rather than a
  // candidate published with no notes.
  let notes: string;
  if (existsSync(notesFile)) {
    notes = candidateNotesBody(notesFrom(notesFile));
  } else if (r.nextMajor) {
    notes = unchangedSince(await sinceTag(module, r.root));
  } else {
    throw new Error(`semantic-release's dry run releases nothing, though ${tag} was due; no candidate tagged`);
  }
  const previous = await previousCandidate(tag, r.root);
  if (previous !== undefined) {
    notes = sinceCandidate(previous, await releasesSince(previous, module.prefix, r.root), r.repository) + notes;
  }
  await git(["tag", tag], r.root);
  await r.pushTag(tag);
  await publish(r.github, tag, module.prefix, notes, false, r.root, true, previous);
  await warmGoProxy(tag, r.repository, r.fetchInfo);
  if (r.nextMajor) {
    r.onMajorCandidate(tag);
  }
  return { tag, note: "release candidate published" };
}

// VERSION_HEADING is the heading semantic-release opens a release's notes
// with: the version, linked to a comparison with the last release.
const VERSION_HEADING = /^#{1,2} \[?[0-9]+\.[0-9]+\.[0-9]+[^\n]*\n+/;

// candidateNotesBody is a dry run's notes without their heading: it names
// the next version by the rules on main — 0.118.0 for a candidate of
// 1.0.0 — and links a comparison with a tag that does not exist yet. The
// candidate's own tag titles its release.
export function candidateNotesBody(notes: string): string {
  return notes.replace(VERSION_HEADING, "");
}

// unchangedSince is the notes of a candidate of the next major cut from the
// code of the release last, which nothing since changes enough to release.
export function unchangedSince(last: string): string {
  return `No change since ${last} cuts a release: this candidate is ${last}'s code, as the next major.\n`;
}

// NEXT_MAJOR_MODULE is the one module a candidate of the next major is cut
// for: the nested modules stay on 0.x past limes's 1.0.
const NEXT_MAJOR_MODULE = ROOT_MODULE;

// releaseModule cuts one module's candidate, and says what it did.
export async function releaseModule(r: Release, module: Module): Promise<Outcome> {
  const seeded = await seedLine(r.root, module, r.pushTag);
  if (seeded !== undefined) {
    info(`${module.name}: release line seeded with ${seeded}`);
  }
  if (r.nextMajor && module.name !== NEXT_MAJOR_MODULE) {
    return { note: `a candidate of the next major is cut for ${NEXT_MAJOR_MODULE} alone` };
  }
  return prereleaseModule(r, module);
}

// SUMMARY_HEADER heads the run's summary table.
const SUMMARY_HEADER = ["Module", "Tag", "What happened"];

// releaseAll cuts every module's candidate, then publishes what earlier
// runs left behind, and reports whether every module's went through.
export async function releaseAll(r: Release): Promise<boolean> {
  const modules = modulesReleased(r);
  const { rows, ok: cut } = await releaseEach(r, modules);
  const published = await publishAllLeftBehind(r, modules);
  const ok = cut && published;
  summarize(`### Release candidates\n\n${table(SUMMARY_HEADER, rows)}`);
  // A run that cut nothing says so where it is seen: a green run with no
  // tag otherwise reads as a candidate that went missing.
  if (ok && rows.every(([, tag]) => tag === "")) {
    notice(`nothing pre-released: ${rows.map(([name, , note]) => `${name}: ${note}`).join("; ")}`);
  }
  return ok;
}

// modulesReleased are the modules r cuts candidates of: every one, or the
// one it names, refusing a module that is none.
function modulesReleased(r: Release): Module[] {
  const names = releasedModules(r.root);
  if (r.only !== undefined && !names.includes(r.only)) {
    throw new Error(`no module ${r.only}; known: ${names.join(", ")}`);
  }
  return names.filter((n) => r.only === undefined || n === r.only).map((name) => selectModule(r.root, name));
}

// releaseEach cuts each module's candidate, a failure of one no reason to
// skip the next; rows are the summary's, and ok is false if any failed.
async function releaseEach(r: Release, modules: Module[]): Promise<{ rows: string[][]; ok: boolean }> {
  const rows: string[][] = [];
  let ok = true;
  for (const module of modules) {
    try {
      const outcome = await releaseModule(r, module);
      info(`${module.name}: ${outcome.note}`);
      rows.push([module.name, outcome.tag ?? "", outcome.note]);
    } catch (err) {
      const message = messageOf(err);
      error(`${module.name}: ${message}`);
      rows.push([module.name, "", `failed: ${message}`]);
      ok = false;
    }
  }
  return { rows, ok };
}

// publishAllLeftBehind publishes the releases an earlier run tagged but did
// not publish, for each module; it is false if any failed.
async function publishAllLeftBehind(r: Release, modules: Module[]): Promise<boolean> {
  let ok = true;
  for (const module of modules) {
    try {
      await publishLeftBehind(r.github, module.prefix, r.root);
    } catch (err) {
      error(`${module.name}: publishing what an earlier run left behind: ${messageOf(err)}`);
      ok = false;
    }
  }
  return ok;
}

// messageOf is what err says.
function messageOf(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

async function main(): Promise<boolean> {
  const repository = requireEnv("GITHUB_REPOSITORY");
  const token = requireEnv("GH_TOKEN");
  const options = releaseOptions(optionalEnv);
  return withDeployKey(repository, async (push) => {
    process.env[PUSH_URL_ENV] = push;
    const root = (await git(["rev-parse", "--show-toplevel"])).trim();
    return releaseAll({
      ...options,
      commits: releaseCommits(),
      pushTag: async (tag) => void (await git(["push", "--quiet", push, `refs/tags/${tag}`], root)),
      github: gitHubFor(token, repository),
      repository,
      root,
      candidateNotes: semanticRelease(root, token, [DRY_RUN_ARG]),
      fetchInfo: (url) => fetch(url),
      notesDir: requireEnv("RUNNER_TEMP"),
      onMajorCandidate: (tag) => setOutput(OUTPUT_LIMES_CANDIDATE, tag),
    });
  });
}

// ReleaseOptions are what a run asks: whether it cuts a candidate of the
// next major, and the one module it cuts, if not all.
export type ReleaseOptions = Pick<Release, "nextMajor" | "only">;

// releaseOptions reads a run's ReleaseOptions from env (CHANNEL, MANUAL,
// NEXT_MAJOR, MODULE), refusing any channel but the prerelease one, and a
// candidate not cut by hand.
export function releaseOptions(env: (name: string) => string | undefined): ReleaseOptions {
  const channel = env("CHANNEL");
  if (channel !== Channel.Prerelease) {
    throw new Error(`CHANNEL ${channel ?? "unset"}: release.ts cuts candidates alone; a release goes through release-plan.ts and release-tag.ts`);
  }
  if (env("MANUAL") !== "true") {
    throw new Error("a prerelease is cut by hand only");
  }
  const module = env("MODULE") ?? ALL_MODULES;
  return { nextMajor: env("NEXT_MAJOR") === "true", only: module === ALL_MODULES ? undefined : module };
}

if (import.meta.main) {
  await runMain(main);
}
