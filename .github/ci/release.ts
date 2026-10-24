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
// modules are proposed their requirement of it all the same.

import { existsSync, rmSync } from "node:fs";
import { join } from "node:path";

import { error, info, notice, setOutput, summarize, table, withoutWorkflowCommands } from "./lib/actions.ts";
import { optionalEnv, requireEnv } from "./lib/env.ts";
import { git } from "./lib/git.ts";
import { gitHubFor, type GitHub } from "./lib/github.ts";
import { runMain } from "./lib/main.ts";
import { ROOT_MODULE, releaseCommits, releasedModules, selectModule, type Module, type ReleaseCommits } from "./lib/modules.ts";
import { candidateTag, RC } from "./lib/prerelease.ts";
import { isDue, lastStableTag, loadPolicy, pendingCommits, type Policy } from "./lib/release-policy.ts";
import { newReleaseTag, notesFrom, publish, publishLeftBehind, tagsOnHead, warmGoProxy, type Fetch } from "./lib/releases.ts";
import { runInherited } from "./lib/spawn.ts";
import { jobPassed, runIdOf } from "./ci-run.ts";

// OUTPUT_LIMES_TAG is the step output the job proposing the nested modules'
// requirement reads.
export const OUTPUT_LIMES_TAG = "limes-tag";
// GATED are the nested modules that build against the limes of the same
// commit through a replace, so a change to limes one must follow lands with
// it. Each is released only once its tests against the limes its go.mod
// requires passed in the dispatching run: ci.yaml's released-limes job, one
// matrix entry per module.
export const GATED: readonly string[] = ["pgstore", "redisreplay", "otelmetric"];
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
// REMOTE is where a candidate's tag is pushed.
const REMOTE = "origin";

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
  channel: Channel;
  // only names the one module to release; undefined for every one.
  only: string | undefined;
  // manual: asked for by hand, so the policy's thresholds do not apply.
  manual: boolean;
  policy: Policy;
  commits: ReleaseCommits;
  // pushTag pushes a tag made on the checked-out commit.
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
  const tag = await candidateTag(module, await pendingCommits(module, "HEAD", r.commits, r.root), r.root);
  if (tag === undefined) {
    return { note: `no commit since ${await sinceTag(module, r.root)} cuts a release, so no candidate` };
  }
  // Read before the tag is made: the candidate's notes are those of the
  // release it stands for, from the last release on.
  const notesFile = join(r.notesDir, `release-notes-${module.name}-${RC}.md`);
  // A file from an earlier run would read as this dry run's notes.
  rmSync(notesFile, { force: true });
  await r.candidateNotes(module, notesFile);
  // No notes: semantic-release found nothing to release where the policy
  // found a candidate, and the two disagree on what is pending. Nothing is
  // tagged rather than a candidate published with no notes.
  if (!existsSync(notesFile)) {
    throw new Error(`${module.name}: semantic-release's dry run releases nothing, though ${tag} was due; no candidate tagged`);
  }
  await git(["tag", tag], r.root);
  await r.pushTag(tag);
  await publish(r.github, tag, module.prefix, notesFrom(notesFile), false, r.root, true);
  await warmGoProxy(tag, r.repository, r.fetchInfo);
  return { tag, note: "release candidate published" };
}

// releaseModule releases one module, and says what it did.
export async function releaseModule(r: Release, module: Module, before: readonly string[]): Promise<Outcome> {
  const gate = GATES[module.name];
  if (gate !== undefined && !(await r.gatePassed(gate))) {
    return { note: `'${gate}' did not pass; not released this run` };
  }
  if (r.channel === Channel.Prerelease) {
    return prereleaseModule(r, module);
  }
  if (!r.manual) {
    const due = isDue(await pendingCommits(module, "HEAD", r.commits, r.root), r.policy);
    if (!due.due) {
      return { note: due.reason };
    }
  }
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
  await warmGoProxy(tag, r.repository, r.fetchInfo);
  return { tag, note: r.manual ? "released by hand" : "released" };
}

// SUMMARY_HEADER heads the run's summary table.
const SUMMARY_HEADER = ["Module", "Tag", "What happened"];

// releaseAll releases every module, then publishes what earlier runs left
// behind, and reports whether every module's release went through.
export async function releaseAll(r: Release): Promise<boolean> {
  const before = await tagsOnHead(r.root);
  let ok = true;
  const names = releasedModules(r.root);
  if (r.only !== undefined && !names.includes(r.only)) {
    throw new Error(`no module ${r.only}; known: ${names.join(", ")}`);
  }
  const modules = names.filter((n) => r.only === undefined || n === r.only).map((name) => selectModule(r.root, name));
  const rows: string[][] = [];
  for (const module of modules) {
    try {
      const outcome = await releaseModule(r, module, before);
      info(`${module.name}: ${outcome.note}`);
      rows.push([module.name, outcome.tag ?? "", outcome.note]);
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      error(`${module.name}: ${message}`);
      rows.push([module.name, "", `failed: ${message}`]);
      ok = false;
    }
  }
  for (const module of modules) {
    try {
      await publishLeftBehind(r.github, module.prefix, r.root);
    } catch (err) {
      error(`${module.name}: publishing what an earlier run left behind: ${err instanceof Error ? err.message : String(err)}`);
      ok = false;
    }
  }
  summarize(`### Release (${r.channel}${r.manual ? ", by hand" : ""})\n\n${table(SUMMARY_HEADER, rows)}`);
  // A run by hand that released nothing says so where it is seen: a green run
  // with no tag otherwise reads as a release that went missing.
  if (r.manual && ok && rows.every(([, tag]) => tag === "")) {
    notice(`nothing ${r.channel === Channel.Prerelease ? "pre-" : ""}released: ${rows.map(([name, , note]) => `${name}: ${note}`).join("; ")}`);
  }
  return ok;
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

async function main(): Promise<void> {
  const repository = requireEnv("GITHUB_REPOSITORY");
  const token = requireEnv("GH_TOKEN");
  const runId = runIdOf(requireEnv("CI_RUN"));
  const github = gitHubFor(token, repository);
  const root = (await git(["rev-parse", "--show-toplevel"])).trim();
  const channel = optionalEnv("CHANNEL") ?? Channel.Release;
  if (channel !== Channel.Release && channel !== Channel.Prerelease) {
    throw new Error(`CHANNEL ${channel} is neither ${Channel.Release} nor ${Channel.Prerelease}`);
  }
  const manual = optionalEnv("MANUAL") === "true";
  if (channel === Channel.Prerelease && !manual) {
    throw new Error("a prerelease is cut by hand only");
  }
  const module = optionalEnv("MODULE") ?? ALL_MODULES;
  const ok = await releaseAll({
    channel,
    manual,
    only: module === ALL_MODULES ? undefined : module,
    policy: loadPolicy(root),
    commits: releaseCommits(),
    pushTag: async (tag) => void (await git(["push", "--quiet", REMOTE, `refs/tags/${tag}`], root)),
    github,
    repository,
    root,
    runSemanticRelease: semanticRelease(root, token),
    candidateNotes: semanticRelease(root, token, [DRY_RUN_ARG]),
    gatePassed: (job) => jobPassed(github, runId, job),
    fetchInfo: (url) => fetch(url),
    notesDir: requireEnv("RUNNER_TEMP"),
    onRootTag: (tag) => setOutput(OUTPUT_LIMES_TAG, tag),
  });
  if (!ok) {
    process.exitCode = 1;
  }
}

if (import.meta.main) {
  await runMain(main);
}
