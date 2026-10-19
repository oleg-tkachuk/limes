// Propose that each nested module require the limes release a run cut.
//
// Usage: GH_TOKEN=… GITHUB_REPOSITORY=… node require-limes.ts <limes tag>
//
// A nested module requires a released limes, and pgstore is released only
// once its tests against the limes it requires pass (release.ts). So every
// limes release is followed by raising that requirement: for each nested
// module of .github/release/modules.cjs, this commits it on a branch of the
// module's own and opens its pull request, or updates the one still open to
// the newer release. auto-merge.yaml merges it once green, and its merge
// releases the module: a build commit scoped to a nested module is a patch of
// it (modules.cjs).
//
// The module's go.mod is read and written by `go mod edit`, then tidied. A
// module that does not require limes, or already requires this release or a
// later one, is left alone. Each of FOLLOWERS is tidied in the same commit.
// Only a pull request from this repository's branch is updated — a fork's of
// the same name never is — and one module's failure leaves the others
// proposed, and fails the run.
//
// Without GH_TOKEN it warns and does nothing: a push made with the
// workflow's own GITHUB_TOKEN would start no CI on the branch
// (auto-merge.yaml). The token reaches only the push and the API: go, which
// fetches modules and may run their version-control tools, never sees it.

import { existsSync } from "node:fs";
import { join } from "node:path";

import { error, info, warning } from "./lib/actions.ts";
import { execute } from "./lib/exec.ts";
import { optionalEnv, requireEnv } from "./lib/env.ts";
import { git } from "./lib/git.ts";
import { gitHubFor, type GitHub } from "./lib/github.ts";
import { runMain } from "./lib/main.ts";
import { ROOT_MODULE, releasedModules, selectModule } from "./lib/modules.ts";

// LIMES_MODULE is the module every nested module requires.
export const LIMES_MODULE = "github.com/oleg-tkachuk/limes";
// FOLLOWERS are the modules never released that build against the nested
// ones through a replace. Raising a nested module's requirement of limes
// raises theirs, so their go.mod, tidied, follows in the same commit: left
// behind, their own check that go.mod is tidy fails the pull request.
export const FOLLOWERS: readonly string[] = ["examples"];
// BRANCH_PREFIX starts the branch a module's requirement is proposed on.
export const BRANCH_PREFIX = "require-limes/";
// BASE_BRANCH is the branch the pull request merges into.
export const BASE_BRANCH = "main";
const REMOTE = "origin";
const RELEASE_TAG = /^v[0-9]+\.[0-9]+\.[0-9]+$/;
// GITHUB_HOST is where the push authenticates.
const GITHUB_HOST = "https://github.com/";

// Proposal is what one module's run did.
export const Proposal = { Opened: "opened", Updated: "updated", Unchanged: "unchanged", NoRequirement: "no requirement" } as const;
export type Proposal = (typeof Proposal)[keyof typeof Proposal];

// Context is where a proposal runs and how it pushes.
export interface Context {
  github: GitHub;
  root: string;
  // push pushes branch to the remote, as the token's owner.
  push(branch: string): Promise<void>;
}

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
async function requiredLimes(dir: string): Promise<string | undefined> {
  const mod = JSON.parse(await go(["mod", "edit", "-json"], dir)) as { Require?: { Path: string; Version: string }[] };
  return mod.Require?.find((r) => r.Path === LIMES_MODULE)?.Version;
}

// compareVersions orders two vX.Y.Z versions.
export function compareVersions(a: string, b: string): number {
  const parts = (v: string) => v.replace(/^v/, "").split(/[.-]/).slice(0, 3).map(Number);
  const [pa, pb] = [parts(a), parts(b)];
  for (let i = 0; i < 3; i++) {
    const d = (pa[i] ?? 0) - (pb[i] ?? 0);
    if (d !== 0) return d;
  }
  return 0;
}

// proposeRequirement raises the module in dir to tag, on its own branch cut
// from main as the remote has it now, and opens or updates its pull request.
export async function proposeRequirement(ctx: Context, dir: string, tag: string): Promise<Proposal> {
  if (!RELEASE_TAG.test(tag)) {
    throw new Error(`limes tag ${JSON.stringify(tag)} is not a release's vX.Y.Z`);
  }
  const moduleDir = join(ctx.root, dir);
  if (dir === "" || !existsSync(join(moduleDir, "go.mod"))) {
    throw new Error(`${JSON.stringify(dir)} is no module`);
  }
  // Fetched afresh: a pull request raising the same module's requirement may
  // have merged since, and a branch cut from before it would conflict with it.
  const branch = `${BRANCH_PREFIX}${dir}`;
  await git(["fetch", "--quiet", REMOTE, BASE_BRANCH], ctx.root);
  await git(["switch", "--quiet", "--discard-changes", "-C", branch, `${REMOTE}/${BASE_BRANCH}`], ctx.root);
  const current = await requiredLimes(moduleDir);
  if (current === undefined) {
    info(`${dir} does not require ${LIMES_MODULE}`);
    return Proposal.NoRequirement;
  }
  if (compareVersions(current, tag) >= 0) {
    info(`${dir} already requires limes ${current}`);
    return Proposal.Unchanged;
  }
  const title = `build(${dir}): require limes ${tag}`;
  await go(["mod", "edit", `-require=${LIMES_MODULE}@${tag}`], moduleDir);
  await go(["mod", "tidy"], moduleDir);
  const changed = [join(dir, "go.mod"), join(dir, "go.sum")];
  for (const follower of FOLLOWERS) {
    if (existsSync(join(ctx.root, follower, "go.mod"))) {
      await go(["mod", "tidy"], join(ctx.root, follower));
      changed.push(join(follower, "go.mod"));
      if (existsSync(join(ctx.root, follower, "go.sum"))) {
        changed.push(join(follower, "go.sum"));
      }
    }
  }
  await git(["add", "--", ...changed.filter((p) => existsSync(join(ctx.root, p)))], ctx.root);
  await git(["commit", "--quiet", "-m", title], ctx.root);
  await ctx.push(branch);
  const text = {
    title,
    body: `limes ${tag} is released. ${dir} required ${current}; this raises it, so ${dir} is tested against, and released with, the limes it now requires.`,
  };
  const open = await ctx.github.sameRepoPullRequest(branch, BASE_BRANCH);
  if (open !== undefined) {
    await ctx.github.editPullRequest(open, text);
    info(`updated #${open}: ${title}`);
    return Proposal.Updated;
  }
  const number = await ctx.github.createPullRequest(branch, BASE_BRANCH, text);
  info(`opened #${number}: ${title}`);
  return Proposal.Opened;
}

// proposeAll proposes tag to every nested module, and reports whether every
// proposal went through.
export async function proposeAll(ctx: Context, tag: string): Promise<boolean> {
  let ok = true;
  for (const name of releasedModules(ctx.root).filter((m) => m !== ROOT_MODULE)) {
    try {
      await proposeRequirement(ctx, selectModule(ctx.root, name).dir, tag);
    } catch (err) {
      error(`${name}: ${err instanceof Error ? err.message : String(err)}`);
      ok = false;
    }
  }
  return ok;
}

// pushAs pushes with token, given to git through the environment of the
// push alone: no credential is written to a config file, and none reaches
// an argument list.
export function pushAs(token: string, repository: string, root: string): Context["push"] {
  const header = `AUTHORIZATION: basic ${Buffer.from(`x-access-token:${token}`).toString("base64")}`;
  return async (branch) => {
    await execute("git", ["push", "--quiet", "--force", `${GITHUB_HOST}${repository}.git`, `HEAD:refs/heads/${branch}`], {
      cwd: root,
      env: { ...process.env, GIT_CONFIG_COUNT: "1", GIT_CONFIG_KEY_0: `http.${GITHUB_HOST}.extraheader`, GIT_CONFIG_VALUE_0: header },
    });
  };
}

async function main(): Promise<void> {
  const [tag = ""] = process.argv.slice(2);
  const token = optionalEnv("GH_TOKEN");
  if (token === undefined) {
    warning(`AUTOMERGE_TOKEN is not set; the nested modules' requirement of limes ${tag} is not proposed (see release.yaml)`);
    return;
  }
  const repository = requireEnv("GITHUB_REPOSITORY");
  const root = (await git(["rev-parse", "--show-toplevel"])).trim();
  const ctx: Context = { github: gitHubFor(token, repository), root, push: pushAs(token, repository, root) };
  if (!(await proposeAll(ctx, tag))) {
    process.exitCode = 1;
  }
}

if (import.meta.main) {
  await runMain(main);
}
