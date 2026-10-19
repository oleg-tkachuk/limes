import assert from "node:assert/strict";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, afterEach, before, beforeEach, describe, test } from "node:test";

import { fakeData, fakeGitHub, type FakeData } from "./lib/fake-github.ts";
import { git } from "./lib/git.ts";
import { tempRepo, type TempRepo } from "./lib/temp-repo.ts";
import { BRANCH_PREFIX, LIMES_MODULE, Proposal, compareVersions, goEnv, proposeRequirement, type Context } from "./require-limes.ts";

const MODULE = "nested";
const FOLLOWER = "examples";
const REQUIRED = "v0.1.0";
const BRANCH = `${BRANCH_PREFIX}${MODULE}`;

// GO_OFFLINE lets go tidy a module that requires limes through a replace
// with no network, and git commit with no identity of the machine's.
const GO_OFFLINE: Record<string, string> = {
  GOFLAGS: "-mod=mod",
  GOPROXY: "off",
  GOWORK: "off",
  GOTOOLCHAIN: "local",
  GIT_AUTHOR_NAME: "test",
  GIT_AUTHOR_EMAIL: "test@example.com",
  GIT_COMMITTER_NAME: "test",
  GIT_COMMITTER_EMAIL: "test@example.com",
};

test("compareVersions", () => {
  assert.ok(compareVersions("v0.2.0", "v0.1.9") > 0);
  assert.ok(compareVersions("v0.10.0", "v0.9.0") > 0);
  assert.equal(compareVersions("v1.0.0", "v1.0.0"), 0);
  assert.ok(compareVersions("v0.1.0", "v0.2.0") < 0);
});

test("go never sees the token", () => {
  process.env.GH_TOKEN = "secret";
  try {
    assert.equal(goEnv().GH_TOKEN, undefined);
  } finally {
    delete process.env.GH_TOKEN;
  }
});

describe("proposeRequirement", () => {
  let work: string;
  let origin: string;
  let repo: TempRepo;
  let data: FakeData;
  const saved: Record<string, string | undefined> = {};

  before(() => {
    for (const [k, v] of Object.entries(GO_OFFLINE)) {
      saved[k] = process.env[k];
      process.env[k] = v;
    }
  });
  after(() => {
    for (const [k, v] of Object.entries(saved)) {
      if (v === undefined) delete process.env[k];
      else process.env[k] = v;
    }
  });

  // scratch is a checkout of main whose origin is a bare repository beside
  // it: a stub limes at the root, MODULE requiring it at required (or not at
  // all), and with follower, FOLLOWER building against both.
  async function scratch(required: string | undefined, follower?: string): Promise<void> {
    work = mkdtempSync(join(tmpdir(), "limes-require-"));
    origin = join(work, "origin.git");
    await git(["init", "--quiet", "--bare", "--initial-branch=main", origin]);
    repo = await tempRepo();
    await repo.run("remote", "add", "origin", origin);
    repo.write("go.mod", `module ${LIMES_MODULE}\n\ngo 1.21\n`);
    repo.write("limes.go", "package limes\n");
    if (required !== undefined) {
      repo.write(`${MODULE}/go.mod`, `module example.com/${MODULE}\n\ngo 1.21\n\nrequire ${LIMES_MODULE} ${required}\n\nreplace ${LIMES_MODULE} => ../\n`);
      repo.write(`${MODULE}/${MODULE}.go`, `package ${MODULE}\n\nimport _ "${LIMES_MODULE}"\n`);
    } else {
      repo.write(`${MODULE}/go.mod`, `module example.com/${MODULE}\n\ngo 1.21\n`);
    }
    repo.write(`${MODULE}/go.sum`, "");
    if (follower !== undefined) {
      repo.write(
        `${FOLLOWER}/go.mod`,
        `module example.com/${FOLLOWER}\n\ngo 1.21\n\nrequire (\n\t${LIMES_MODULE} ${follower}\n\texample.com/${MODULE} ${follower}\n)\n\nreplace (\n\t${LIMES_MODULE} => ../\n\texample.com/${MODULE} => ../${MODULE}\n)\n`,
      );
      repo.write(`${FOLLOWER}/main.go`, `package main\n\nimport _ "example.com/${MODULE}"\n\nfunc main() {}\n`);
    }
    await repo.commit("init");
    await repo.run("push", "--quiet", "origin", "main");
    data = fakeData();
  }

  function ctx(): Context {
    return { github: fakeGitHub(data), root: repo.dir, push: async (branch) => void (await repo.run("push", "--quiet", "--force", "origin", `HEAD:refs/heads/${branch}`)) };
  }

  // requiredOn is the limes version a go.mod requires on the origin branch.
  async function requiredOn(branch: string, path: string): Promise<string | undefined> {
    const mod = await git(["show", `${branch}:${path}`], origin);
    return new RegExp(`${LIMES_MODULE.replaceAll(".", "\\.")} (v[0-9.]+)`).exec(mod)?.[1];
  }

  beforeEach(() => undefined);
  afterEach(() => {
    repo?.cleanup();
    rmSync(work, { recursive: true, force: true });
  });

  test("a release past what the module requires: its own branch, one commit, a pull request", async () => {
    await scratch(REQUIRED);
    assert.equal(await proposeRequirement(ctx(), MODULE, "v0.2.0"), Proposal.Opened);
    assert.equal(await requiredOn(BRANCH, `${MODULE}/go.mod`), "v0.2.0");
    assert.equal((await git(["rev-list", "--count", `main..${BRANCH}`], origin)).trim(), "1");
    assert.deepEqual(data.createdPulls.map((p) => [p.head, p.base, p.text.title]), [[BRANCH, "main", `build(${MODULE}): require limes v0.2.0`]]);
  });

  test("a follower is tidied in the same commit", async () => {
    await scratch(REQUIRED, REQUIRED);
    await proposeRequirement(ctx(), MODULE, "v0.2.0");
    assert.equal(await requiredOn(BRANCH, `${FOLLOWER}/go.mod`), "v0.2.0");
  });

  test("a newer release while the pull request is open: retitled, not opened again", async () => {
    await scratch(REQUIRED);
    await proposeRequirement(ctx(), MODULE, "v0.2.0");
    assert.equal(await proposeRequirement(ctx(), MODULE, "v0.3.0"), Proposal.Updated);
    assert.equal(data.createdPulls.length, 1);
    assert.equal(data.editedPulls[0]?.text.title, `build(${MODULE}): require limes v0.3.0`);
    assert.equal(await requiredOn(BRANCH, `${MODULE}/go.mod`), "v0.3.0");
  });

  test("main moved on the remote since the checkout fetched: the branch is cut from it", async () => {
    await scratch(REQUIRED);
    const other = await tempRepo();
    try {
      await git(["clone", "--quiet", origin, join(other.dir, "c")]);
      await git(["-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "moved"], join(other.dir, "c"));
      await git(["push", "--quiet", "origin", "main"], join(other.dir, "c"));
    } finally {
      other.cleanup();
    }
    await proposeRequirement(ctx(), MODULE, "v0.2.0");
    await git(["merge-base", "--is-ancestor", "main", BRANCH], origin);
  });

  test("nothing to raise", async () => {
    await scratch(REQUIRED);
    assert.equal(await proposeRequirement(ctx(), MODULE, REQUIRED), Proposal.Unchanged, "the same release");
    assert.equal(await proposeRequirement(ctx(), MODULE, "v0.0.9"), Proposal.Unchanged, "an older one");
    repo.cleanup();
    rmSync(work, { recursive: true, force: true });
    await scratch(undefined);
    assert.equal(await proposeRequirement(ctx(), MODULE, "v0.2.0"), Proposal.NoRequirement, "no requirement");
    assert.deepEqual(data.createdPulls, []);
  });

  test("not a release's tag, or no module", async () => {
    await scratch(REQUIRED);
    for (const tag of ["latest", "v0.2", "0.2.0", "v0.2.0-rc.1"]) {
      await assert.rejects(proposeRequirement(ctx(), MODULE, tag), Error, tag);
    }
    await assert.rejects(proposeRequirement(ctx(), "missing", "v0.2.0"));
    await assert.rejects(proposeRequirement(ctx(), "", "v0.2.0"));
  });
});
