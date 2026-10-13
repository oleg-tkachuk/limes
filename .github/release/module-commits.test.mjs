// module-commits.test.mjs — each module is released from the commits that
// touch it, and from no others.
//
// A commit counted for the wrong module is silent: a change to one module
// cuts a release of another, or none of its own. The cases below run the
// plugin over a scratch repository holding a root module and a nested one.
//
// Runs from `task verify-all` after `npm ci`.

import { createRequire } from "node:module";
import { execFileSync } from "node:child_process";
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";

const require = createRequire(import.meta.url);
const plugin = require("./module-commits.cjs");
const modules = require("./modules.cjs");

// The nested module of the scratch repository: the real one, so a rename in
// modules.cjs is followed here.
const NESTED = "redisreplay";
const root = modules.configFor(modules.ROOT_MODULE);
const nested = modules.configFor(NESTED);
const scopeOf = (cfg) => cfg.plugins.find((p) => Array.isArray(p) && p[0] === "./module-commits.cjs")[1];

const repo = mkdtempSync(join(tmpdir(), "module-commits-"));
const git = (...args) => execFileSync("git", args, { cwd: repo, encoding: "utf8" }).trim();
const IDENTITY = ["-c", "user.name=t", "-c", "user.email=t@example.invalid"];

// commit writes each path and commits them with message; its hash.
function commit(message, ...paths) {
  for (const p of paths) {
    mkdirSync(dirname(join(repo, p)), { recursive: true });
    writeFileSync(join(repo, p), `${message}\n`);
  }
  git("add", "-A");
  git(...IDENTITY, "commit", "-q", "-m", message);
  return { hash: git("rev-parse", "HEAD"), message };
}

const logger = { log() {}, error() {}, warn() {}, success() {} };
let failed = 0;

try {
  git("init", "-q", "-b", "main");
  const first = commit("chore: start", "go.mod", `${NESTED}/go.mod`);
  // A module with no release line: neither the root nor NESTED counts it.
  const unreleased = commit("feat: add a store", `${modules.UNRELEASED_DIRS[0]}/store.go`);
  const rootFeat = commit("feat: add a caveat", "caveat.go");
  const nestedFeat = commit(`feat(${NESTED}): add a cache`, `${NESTED}/cache.go`);
  const both = commit("fix: rename across both", "rename.go", `${NESTED}/rename.go`);
  git("checkout", "-q", "-b", "side");
  const sideFix = commit(`fix(${NESTED}): bound a key`, `${NESTED}/key.go`);
  git("checkout", "-q", "main");
  git(...IDENTITY, "merge", "-q", "--no-ff", "-m", `fix(${NESTED}): merge a fix`, "side");
  const merge = { hash: git("rev-parse", "HEAD"), message: `fix(${NESTED}): merge a fix` };
  const all = [first, unreleased, rootFeat, nestedFeat, both, sideFix, merge];

  // [module, scope, the commits it must count]
  const cases = [
    [modules.ROOT_MODULE, scopeOf(root), [first, rootFeat, both]],
    [NESTED, scopeOf(nested), [first, nestedFeat, both, sideFix, merge]],
  ];
  for (const [name, scope, want] of cases) {
    const got = all.filter((c) => plugin.touches(plugin.changedPaths(repo, c.hash), scope)).map((c) => c.message);
    const wanted = want.map((c) => c.message);
    if (JSON.stringify(got) !== JSON.stringify(wanted)) {
      console.log(`FAIL ${name} counts ${JSON.stringify(got)}, want ${JSON.stringify(wanted)}`);
      failed = 1;
    }
  }

  // Through the analyzer: a change to the nested module alone releases
  // nothing of the root, and a merge releases the module it brought a change to.
  const context = (commits) => ({ commits, logger, cwd: repo, env: {}, options: { repositoryUrl: root.repositoryUrl } });
  const releases = [
    [modules.ROOT_MODULE, scopeOf(root), [nestedFeat, merge], null],
    [modules.ROOT_MODULE, scopeOf(root), [rootFeat, nestedFeat], "minor"],
    [NESTED, scopeOf(nested), [rootFeat], null],
    [NESTED, scopeOf(nested), [merge], "patch"],
  ];
  for (const [name, scope, commits, want] of releases) {
    const got = (await plugin.analyzeCommits(scope, context(commits))) ?? null;
    if (got !== want) {
      console.log(`FAIL ${name} over ${JSON.stringify(commits.map((c) => c.message))}: got ${got}, want ${want}`);
      failed = 1;
    }
  }

  // The notes of a module list its own changes and not another's.
  const lastRelease = { gitTag: `${NESTED}/v0.1.0`, version: "0.1.0" };
  const nextRelease = { gitTag: `${NESTED}/v0.2.0`, version: "0.2.0", type: "minor" };
  const notes = await plugin.generateNotes(scopeOf(nested), { ...context(all), lastRelease, nextRelease });
  if (!notes.includes("add a cache") || notes.includes("add a caveat")) {
    console.log(`FAIL the ${NESTED} notes hold another module's change, or lack their own:\n${notes}`);
    failed = 1;
  }
} finally {
  rmSync(repo, { recursive: true, force: true });
}

if (failed) process.exit(1);
console.log("module commits: every module counts its own commits");
