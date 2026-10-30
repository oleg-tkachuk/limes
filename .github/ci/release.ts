// Release every module the repository releases, each on its own line.
//
// Usage: GH_TOKEN=… GITHUB_REPOSITORY=… CI_RUN=<run id> RUNNER_TEMP=…
//        [CHANNEL=release|prerelease] [MODULE=all|<name>] [MANUAL=true] node release.ts
//
// A release nobody asked for by hand — CI's dispatch after a green push —
// releases a module only once its pending commits are due by the policy in
// .github/release/release-policy.json (lib/release-policy.ts). A release by
// hand (MANUAL) releases whatever is pending. A prerelease, by hand only,
// tags the version the next release would be as -rc.N on the checked-out
// commit and publishes it as a GitHub prerelease. MODULE narrows either to
// one module.
//
// Run from release.yaml once the dispatching CI run vouched for the commit
// and the head was found releasable (release-head.ts). For each module of
// .github/release/modules.cjs, in its order, semantic-release computes and
// pushes the module's tag from the commits that touch it, and the tag is
// published as a GitHub release: the notes semantic-release generated —
// breaking changes first — then GitHub's list of the pull requests merged
// since the module's previous tag, and the Go proxy is told of it. A module
// whose CI job against the released limes did not pass (GATES) is not
// released this run: it waits, released with everything since, for the pull
// request that raises its requirement. One module's failure leaves the
// others released, and fails the run. Last, every recent tag an earlier run
// pushed and never published is published.
//
// The root module's tag is set as the step output `limes-tag` as soon as it
// is known, before it is published: a re-run publishing a tag an earlier
// attempt pushed finds it only while it has no release, and the nested
// modules are proposed their requirement of it all the same. A candidate of
// the next major is set as `limes-candidate` once published, and its row in
// the migration guide is proposed (candidates.ts). Every release
// published sets `released`, and the modules' changelogs are proposed their
// new entries (changelog.ts).

import { existsSync, mkdtempSync, rmSync } from "node:fs";
import { join } from "node:path";

import { DEPLOY_KEY_ENV, pushUrl, useDeployKey } from "./lib/deploy-key.ts";
import { error, info, notice, setOutput, summarize, table, withoutWorkflowCommands } from "./lib/actions.ts";
import { optionalEnv, requireEnv } from "./lib/env.ts";
import { git } from "./lib/git.ts";
import { gitHubFor, type GitHub } from "./lib/github.ts";
import { runMain } from "./lib/main.ts";
import { ROOT_MODULE, releaseCommits, releasedModules, selectModule, type Module, type ReleaseCommits } from "./lib/modules.ts";
import { candidateTag, previousCandidate, RC, releasesSince, sinceCandidate } from "./lib/prerelease.ts";
import { isDue, lastStableTag, loadPolicy, pendingCommits, type Policy } from "./lib/release-policy.ts";
import { newReleaseTag, notesFrom, publish, publishLeftBehind, tagsOnHead, warmGoProxy, type Fetch } from "./lib/releases.ts";
import { runInherited } from "./lib/spawn.ts";
import { jobPassed, runIdOf } from "./ci-run.ts";

// OUTPUT_LIMES_TAG is the step output the job proposing the nested modules'
// requirement reads.
export const OUTPUT_LIMES_TAG = "limes-tag";
// OUTPUT_LIMES_CANDIDATE is the step output the job proposing the
// candidate's row in the migration guide reads (candidates.ts).
export const OUTPUT_LIMES_CANDIDATE = "limes-candidate";
// OUTPUT_RELEASED is the step output the job proposing the changelogs' new
// entries reads: true once a release of any module is published.
export const OUTPUT_RELEASED = "released";
// GATED are the nested modules that build against the limes of the same
// commit through a replace, so a change to limes one must follow lands with
// it. Each is released only once its tests against the limes its go.mod
// requires passed in the dispatching run: ci.yaml's released-limes job, one
// matrix entry per module.
export const GATED: readonly string[] = ["pgstore", "redisreplay", "otelmetric", "exchange", "biscuit"];
// gateOf is the name of the released-limes job of module, as GitHub names
// a matrix job whose name holds no matrix value: ci.yaml's name, then the
// module in parentheses.
export function gateOf(module: string): string {
  return `Released limes (${module})`;
}
// GATES names, by module, the CI job that must have passed in the
// dispatching run for the module to be released.
export const GATES: Readonly<Record<string, string>> = Object.fromEntries(GATED.map((m) => [m, gateOf(m)]));
// Channel is what a run releases: releases, or release candidates.
export const Channel = { Release: "release", Prerelease: "prerelease" } as const;
export type Channel = (typeof Channel)[keyof typeof Channel];
// ALL_MODULES releases every module.
export const ALL_MODULES = "all";
// PUSH_URL_ENV hands semantic-release the URL it pushes a tag to:
// modules.cjs reads it as repositoryUrl, and release.test.ts holds the two
// to one name.
export const PUSH_URL_ENV = "RELEASE_PUSH_URL";
// SSH_DIR_PREFIX names the directory the deploy key is written to, under
// the runner's temporary directory.
const SSH_DIR_PREFIX = "release-ssh-";

// RELEASE_DIR is where semantic-release runs, beside its config and lockfile.
const RELEASE_DIR = ".github/release";
// MODULE_ENV picks the module semantic-release releases (modules.cjs).
const MODULE_ENV = "RELEASE_MODULE";
// NOTES_ENV names the file release-notes-file.cjs writes the notes to.
const NOTES_ENV = "RELEASE_NOTES_FILE";

// Release is everything one run needs, its effects injected so a test can
// stand in for semantic-release, GitHub and the Go proxy.
export interface Release {
  github: GitHub;
  repository: string;
  root: string;
  // runSemanticRelease computes and pushes module's tag, writing its notes
  // to notesFile.
  runSemanticRelease(module: Module, notesFile: string): Promise<void>;
  // candidateNotes writes to notesFile the notes module's next release
  // would have, from a dry run that tags nothing, and nothing when no
  // release is due.
  candidateNotes(module: Module, notesFile: string): Promise<void>;
  // gatePassed reports whether a gating CI job passed.
  gatePassed(job: string): Promise<boolean>;
  fetchInfo: Fetch;
  notesDir: string;
  // onRootTag is told the root module's tag, before it is published.
  onRootTag(tag: string): void;
  // onMajorCandidate is told the tag of a candidate of the next major, once
  // it is published.
  onMajorCandidate(tag: string): void;
  // onReleased is told each module's release tag, once it is published.
  onReleased(tag: string): void;
  channel: Channel;
  // only names the one module to release; undefined for every one.
  only: string | undefined;
  // manual: asked for by hand, so the policy's thresholds do not apply.
  manual: boolean;
  // nextMajor: a prerelease run cutting a candidate of limes's next major,
  // v1.0.0-rc.N, rather than of its next version by the pending commits.
  // Between two such candidates the API may still break: it is 0.x on main
  // until the release that cuts 1.0.
  nextMajor: boolean;
  policy: Policy;
  commits: ReleaseCommits;
  // pushTag pushes a tag made in the checkout.
  pushTag(tag: string): Promise<void>;
}

// Outcome is what a run did with one module: the tag it released, if any,
// and why.
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

// SEED_VERSION is where a nested module's release line starts: semantic-
// release cuts a module with no tag as 1.0.0, which major-guard refuses.
export const SEED_VERSION = "v0.1.0";

// GO_MOD is the file whose first commit holds a module.
const GO_MOD = "go.mod";

// seedLine starts the release line of a nested module no tag names yet,
// with a SEED_VERSION tag on the commit that added its go.mod, pushed with
// the deploy key, which alone may create a release tag. It returns the tag,
// or undefined for the root module, one already tagged, or one whose go.mod
// no commit added.
export async function seedLine(r: Release, module: Module): Promise<string | undefined> {
  if (module.name === ROOT_MODULE || (await git(["tag", "--list", `${module.prefix}v*`], r.root)).trim() !== "") {
    return undefined;
  }
  const added = (await git(["log", "--diff-filter=A", "--format=%H", "--", join(module.dir, GO_MOD)], r.root)).trim().split("\n").at(-1);
  if (!added) {
    return undefined;
  }
  const tag = `${module.prefix}${SEED_VERSION}`;
  await git(["tag", tag, added], r.root);
  await r.pushTag(tag);
  return tag;
}

// releaseModule releases one module, and says what it did.
export async function releaseModule(r: Release, module: Module, before: readonly string[]): Promise<Outcome> {
  const seeded = await seedLine(r, module);
  if (seeded !== undefined) {
    info(`${module.name}: release line seeded with ${seeded}`);
  }
  const gate = GATES[module.name];
  if (gate !== undefined && !(await r.gatePassed(gate))) {
    return { note: `'${gate}' did not pass; not released this run` };
  }
  if (r.channel === Channel.Prerelease) {
    if (r.nextMajor && module.name !== NEXT_MAJOR_MODULE) {
      return { note: `a candidate of the next major is cut for ${NEXT_MAJOR_MODULE} alone` };
    }
    return prereleaseModule(r, module);
  }
  if (!r.manual) {
    const due = isDue(await pendingCommits(module, "HEAD", r.commits, r.root), r.policy);
    if (!due.due) {
      return { note: due.reason };
    }
  }
  return releaseDue(r, module, before);
}

// releaseDue releases module, due or asked for by hand: its tag, from
// semantic-release, published with its notes and warmed in the Go proxy.
async function releaseDue(r: Release, module: Module, before: readonly string[]): Promise<Outcome> {
  const notesFile = join(r.notesDir, `release-notes-${module.name}.md`);
  await r.runSemanticRelease(module, notesFile);
  const tag = await newReleaseTag(r.github, before, module.prefix, r.root);
  if (tag === undefined) {
    return { note: `no commit since ${await sinceTag(module, r.root)} cuts a release` };
  }
  if (module.name === ROOT_MODULE) {
    r.onRootTag(tag);
  }
  // GitHub decides which root release is Latest, as it always has; a nested
  // module's is never it.
  await publish(r.github, tag, module.prefix, notesFrom(notesFile), module.name === ROOT_MODULE, r.root);
  r.onReleased(tag);
  await warmGoProxy(tag, r.repository, r.fetchInfo);
  return { tag, note: r.manual ? "released by hand" : "released" };
}

// SUMMARY_HEADER heads the run's summary table.
const SUMMARY_HEADER = ["Module", "Tag", "What happened"];

// releaseAll releases every module, then publishes what earlier runs left
// behind, and reports whether every module's release went through.
export async function releaseAll(r: Release): Promise<boolean> {
  const modules = modulesReleased(r);
  const before = await tagsOnHead(r.root);
  const { rows, ok: released } = await releaseEach(r, modules, before);
  const published = await publishAllLeftBehind(r, modules);
  const ok = released && published;
  summarize(`### Release (${r.channel}${r.manual ? ", by hand" : ""})\n\n${table(SUMMARY_HEADER, rows)}`);
  // A run by hand that released nothing says so where it is seen: a green run
  // with no tag otherwise reads as a release that went missing.
  if (r.manual && ok && rows.every(([, tag]) => tag === "")) {
    notice(`nothing ${r.channel === Channel.Prerelease ? "pre-" : ""}released: ${rows.map(([name, , note]) => `${name}: ${note}`).join("; ")}`);
  }
  return ok;
}

// modulesReleased are the modules r releases: every one, or the one it
// names, refusing a module that is none, or a candidate of the next major
// on any channel but the prerelease one.
function modulesReleased(r: Release): Module[] {
  if (r.nextMajor && r.channel !== Channel.Prerelease) {
    throw new Error("a candidate of the next major is a prerelease: run the prerelease channel");
  }
  const names = releasedModules(r.root);
  if (r.only !== undefined && !names.includes(r.only)) {
    throw new Error(`no module ${r.only}; known: ${names.join(", ")}`);
  }
  return names.filter((n) => r.only === undefined || n === r.only).map((name) => selectModule(r.root, name));
}

// releaseEach releases each module, a failure of one no reason to skip the
// next; rows are the summary's, and ok is false if any failed.
async function releaseEach(r: Release, modules: Module[], before: readonly string[]): Promise<{ rows: string[][]; ok: boolean }> {
  const rows: string[][] = [];
  let ok = true;
  for (const module of modules) {
    try {
      const outcome = await releaseModule(r, module, before);
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

// SEMANTIC_RELEASE_BIN is the pinned semantic-release's executable, as npm
// ci installs it beside the release tooling's lockfile.
const SEMANTIC_RELEASE_BIN = ["node_modules", ".bin", "semantic-release"];
// SEMANTIC_RELEASE_ARGS run it outside the CI it would otherwise detect, and
// DRY_RUN_ARG without tagging: a candidate's notes.
const SEMANTIC_RELEASE_ARGS = ["--no-ci"];
const DRY_RUN_ARG = "--dry-run";

// semanticReleaseCommand is the command that runs the pinned semantic-release
// in root's checkout: its installed executable, never npx, which would fetch
// one from the registry were it missing.
export function semanticReleaseCommand(root: string): [string, string[]] {
  return [join(root, RELEASE_DIR, ...SEMANTIC_RELEASE_BIN), [...SEMANTIC_RELEASE_ARGS]];
}

// semanticRelease runs the pinned semantic-release for module, with the
// runner reading no workflow command from its log: it logs every commit
// message it reads, and this job holds a write token.
function semanticRelease(root: string, token: string, extra: readonly string[] = []): Release["runSemanticRelease"] {
  const [file, args] = semanticReleaseCommand(root);
  return (module, notesFile) =>
    withoutWorkflowCommands(() =>
      runInherited(file, [...args, ...extra], {
        cwd: join(root, RELEASE_DIR),
        env: {
          ...process.env,
          GITHUB_TOKEN: token,
          [NOTES_ENV]: notesFile,
          ...(module.name === ROOT_MODULE ? {} : { [MODULE_ENV]: module.name }),
        },
      }),
    );
}

async function main(): Promise<boolean> {
  const repository = requireEnv("GITHUB_REPOSITORY");
  const token = requireEnv("GH_TOKEN");
  // Every tag goes over SSH with the deploy key, which alone may create a
  // release tag (repo-settings.ts); the key's text leaves the environment
  // before any child process starts.
  const sshDir = mkdtempSync(join(requireEnv("RUNNER_TEMP"), SSH_DIR_PREFIX));
  process.env.GIT_SSH_COMMAND = useDeployKey(requireEnv(DEPLOY_KEY_ENV), sshDir);
  delete process.env[DEPLOY_KEY_ENV];
  const push = pushUrl(repository);
  process.env[PUSH_URL_ENV] = push;
  try {
    return await releaseFrom(repository, token, push);
  } finally {
    rmSync(sshDir, { recursive: true, force: true });
  }
}

async function releaseFrom(repository: string, token: string, push: string): Promise<boolean> {
  const runId = runIdOf(requireEnv("CI_RUN"));
  const github = gitHubFor(token, repository);
  const root = (await git(["rev-parse", "--show-toplevel"])).trim();
  const ok = await releaseAll({
    ...releaseOptions(optionalEnv),
    policy: loadPolicy(root),
    commits: releaseCommits(),
    pushTag: async (tag) => void (await git(["push", "--quiet", push, `refs/tags/${tag}`], root)),
    github,
    repository,
    root,
    runSemanticRelease: semanticRelease(root, token),
    candidateNotes: semanticRelease(root, token, [DRY_RUN_ARG]),
    gatePassed: (job) => jobPassed(github, runId, job),
    fetchInfo: (url) => fetch(url),
    notesDir: requireEnv("RUNNER_TEMP"),
    onRootTag: (tag) => setOutput(OUTPUT_LIMES_TAG, tag),
    onMajorCandidate: (tag) => setOutput(OUTPUT_LIMES_CANDIDATE, tag),
    onReleased: () => setOutput(OUTPUT_RELEASED, String(true)),
  });
  return ok;
}

// ReleaseOptions are what a run asks of the release: its channel, whether
// it is by hand, whether it cuts a candidate of the next major, and the one
// module it releases, if not all.
export type ReleaseOptions = Pick<Release, "channel" | "manual" | "nextMajor" | "only">;

// releaseOptions reads a run's ReleaseOptions from env (CHANNEL, MANUAL,
// NEXT_MAJOR, MODULE), refusing a channel this script does not cut, or a
// prerelease not cut by hand.
export function releaseOptions(env: (name: string) => string | undefined): ReleaseOptions {
  const channel = env("CHANNEL") ?? Channel.Release;
  if (channel !== Channel.Release && channel !== Channel.Prerelease) {
    throw new Error(`CHANNEL ${channel} is neither ${Channel.Release} nor ${Channel.Prerelease}`);
  }
  const manual = env("MANUAL") === "true";
  if (channel === Channel.Prerelease && !manual) {
    throw new Error("a prerelease is cut by hand only");
  }
  const module = env("MODULE") ?? ALL_MODULES;
  return { channel, manual, nextMajor: env("NEXT_MAJOR") === "true", only: module === ALL_MODULES ? undefined : module };
}

if (import.meta.main) {
  await runMain(main);
}
