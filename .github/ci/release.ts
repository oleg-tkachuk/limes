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

import { join } from "node:path";

import { error, info, setOutput, withoutWorkflowCommands } from "./lib/actions.ts";
import { optionalEnv, requireEnv } from "./lib/env.ts";
import { git } from "./lib/git.ts";
import { gitHubFor, type GitHub } from "./lib/github.ts";
import { runMain } from "./lib/main.ts";
import { ROOT_MODULE, releaseCommits, releasedModules, selectModule, type Module, type ReleaseCommits } from "./lib/modules.ts";
import { candidateTag } from "./lib/prerelease.ts";
import { isDue, loadPolicy, pendingCommits, type Policy } from "./lib/release-policy.ts";
import { newReleaseTag, notesFrom, publish, publishLeftBehind, tagsOnHead, warmGoProxy, type Fetch } from "./lib/releases.ts";
import { runInherited } from "./lib/spawn.ts";
import { jobPassed, runIdOf } from "./ci-run.ts";

// OUTPUT_LIMES_TAG is the step output the job proposing the nested modules'
// requirement reads.
export const OUTPUT_LIMES_TAG = "limes-tag";
// GATES names, by module, the CI job that must have passed in the
// dispatching run for the module to be released: pgstore's tests against the
// limes its go.mod requires, so a change to limes that pgstore must follow
// lands with pgstore.
export const GATES: Readonly<Record<string, string>> = { pgstore: "PostgreSQL store, released limes" };
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

// prereleaseModule tags and publishes the module's next release candidate,
// and reports its tag, if anything is pending.
export async function prereleaseModule(r: Release, module: Module): Promise<string | undefined> {
  const tag = await candidateTag(module, await pendingCommits(module, "HEAD", r.commits, r.root), r.root);
  if (tag === undefined) {
    info(`${module.name}: nothing pending to release a candidate of`);
    return undefined;
  }
  await git(["tag", tag], r.root);
  await r.pushTag(tag);
  await publish(r.github, tag, module.prefix, "", false, r.root, true);
  await warmGoProxy(tag, r.repository, r.fetchInfo);
  return tag;
}

// releaseModule releases one module, and reports its new tag, if any.
export async function releaseModule(r: Release, module: Module, before: readonly string[]): Promise<string | undefined> {
  const gate = GATES[module.name];
  if (gate !== undefined && !(await r.gatePassed(gate))) {
    info(`${module.name}: '${gate}' did not pass; not released this run`);
    return undefined;
  }
  if (r.channel === Channel.Prerelease) {
    return prereleaseModule(r, module);
  }
  if (!r.manual) {
    const due = isDue(await pendingCommits(module, "HEAD", r.commits, r.root), r.policy);
    info(`${module.name}: ${due.reason}`);
    if (!due.due) {
      return undefined;
    }
  }
  const notesFile = join(r.notesDir, `release-notes-${module.name}.md`);
  await r.runSemanticRelease(module, notesFile);
  const tag = await newReleaseTag(r.github, before, module.prefix, r.root);
  if (tag === undefined) {
    info(`${module.name}: no new tag from this run — no release was due`);
    return undefined;
  }
  if (module.name === ROOT_MODULE) {
    r.onRootTag(tag);
  }
  // GitHub decides which root release is Latest, as it always has; a nested
  // module's is never it.
  await publish(r.github, tag, module.prefix, notesFrom(notesFile), module.name === ROOT_MODULE, r.root);
  await warmGoProxy(tag, r.repository, r.fetchInfo);
  return tag;
}

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
  for (const module of modules) {
    try {
      await releaseModule(r, module, before);
    } catch (err) {
      error(`${module.name}: ${err instanceof Error ? err.message : String(err)}`);
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
  return ok;
}

// semanticRelease runs the pinned semantic-release for module, with the
// runner reading no workflow command from its log: it logs every commit
// message it reads, and this job holds a write token.
function semanticRelease(root: string, token: string): Release["runSemanticRelease"] {
  return (module, notesFile) =>
    withoutWorkflowCommands(() =>
      runInherited("npx", ["semantic-release", "--no-ci"], {
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
