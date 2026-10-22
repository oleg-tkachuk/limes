import assert from "node:assert/strict";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createRequire } from "node:module";
import { after, afterEach, before, beforeEach, describe, test } from "node:test";

import { fakeData, fakeGitHub, type FakeData } from "./lib/fake-github.ts";
import { git } from "./lib/git.ts";
import { tempRepo, type TempRepo } from "./lib/temp-repo.ts";
import { BRANCH, LIMES_MODULE, LIMES_REQUIREMENT_SCOPE, Proposal, compareVersions, goEnv, proposeRequirements, requirementTitle, requirementsSummary, type Context } from "./require-limes.ts";

const MODULE = "nested";
const OTHER = "other";
const FOLLOWER = "examples";
const REQUIRED = "v0.1.0";
const TITLE = (tag: string, ...dirs: string[]) => `build(${LIMES_REQUIREMENT_SCOPE}): require limes ${tag} in ${dirs.join(" and ")}`;

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

// The scope of the commit is the one the nested modules' release rules
// release as a patch: another would merge and release none of them.
test("the commit's scope is the one the release cuts a patch of", () => {
  const modules = createRequire(import.meta.url)("../release/modules.cjs") as { LIMES_REQUIREMENT_SCOPE: string };
  assert.equal(LIMES_REQUIREMENT_SCOPE, modules.LIMES_REQUIREMENT_SCOPE);
});

test("go never sees the token", () => {
  process.env.GH_TOKEN = "secret";
  try {
    assert.equal(goEnv().GH_TOKEN, undefined);
  } finally {
    delete process.env.GH_TOKEN;
  }
});

describe("proposeRequirements", () => {
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
  // all), OTHER at other, when given, and with follower, FOLLOWER building
  // against limes and MODULE.
  async function scratch(required: string | undefined, follower?: string, other?: string): Promise<void> {
    work = mkdtempSync(join(tmpdir(), "limes-require-"));
    origin = join(work, "origin.git");
    await git(["init", "--quiet", "--bare", "--initial-branch=main", origin]);
    repo = await tempRepo();
    await repo.run("remote", "add", "origin", origin);
    repo.write("go.mod", `module ${LIMES_MODULE}\n\ngo 1.21\n`);
    repo.write("limes.go", "package limes\n");
    const nested = (dir: string, version: string | undefined) => {
      if (version !== undefined) {
        repo.write(`${dir}/go.mod`, `module example.com/${dir}\n\ngo 1.21\n\nrequire ${LIMES_MODULE} ${version}\n\nreplace ${LIMES_MODULE} => ../\n`);
        repo.write(`${dir}/${dir}.go`, `package ${dir}\n\nimport _ "${LIMES_MODULE}"\n`);
      } else {
        repo.write(`${dir}/go.mod`, `module example.com/${dir}\n\ngo 1.21\n`);
      }
      repo.write(`${dir}/go.sum`, "");
    };
    nested(MODULE, required);
    if (other !== undefined) {
      nested(OTHER, other);
    }
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

  test("a release past what the modules require: one branch, one commit, one pull request", async () => {
    await scratch(REQUIRED, undefined, REQUIRED);
    const outcome = await proposeRequirements(ctx(), [MODULE, OTHER], "v0.2.0");
    assert.equal(outcome.proposal, Proposal.Opened);
    assert.equal(typeof outcome.pullRequest, "number");
    assert.deepEqual(outcome.raised, [
      { dir: MODULE, from: REQUIRED },
      { dir: OTHER, from: REQUIRED },
    ]);
    assert.equal(await requiredOn(BRANCH, `${MODULE}/go.mod`), "v0.2.0");
    assert.equal(await requiredOn(BRANCH, `${OTHER}/go.mod`), "v0.2.0");
    assert.equal((await git(["rev-list", "--count", `main..${BRANCH}`], origin)).trim(), "1");
    assert.deepEqual(data.createdPulls.map((p) => [p.head, p.base, p.text.title]), [[BRANCH, "main", TITLE("v0.2.0", MODULE, OTHER)]]);
    assert.match(data.createdPulls[0]?.text.body ?? "", new RegExp(`- ${OTHER}: ${REQUIRED} → v0\\.2\\.0`));
  });

  test("a module that requires the release already is left out of it", async () => {
    await scratch(REQUIRED, undefined, "v0.2.0");
    const outcome = await proposeRequirements(ctx(), [MODULE, OTHER], "v0.2.0");
    assert.deepEqual(outcome.raised.map((r) => r.dir), [MODULE]);
    assert.equal((await git(["diff", "--name-only", "main", BRANCH, "--", OTHER], origin)).trim(), "");
  });

  test("a follower is tidied in the same commit", async () => {
    await scratch(REQUIRED, REQUIRED);
    await proposeRequirements(ctx(), [MODULE], "v0.2.0");
    assert.equal(await requiredOn(BRANCH, `${FOLLOWER}/go.mod`), "v0.2.0");
  });

  test("a newer release while the pull request is open: retitled, not opened again", async () => {
    await scratch(REQUIRED);
    await proposeRequirements(ctx(), [MODULE], "v0.2.0");
    assert.equal((await proposeRequirements(ctx(), [MODULE], "v0.3.0")).proposal, Proposal.Updated);
    assert.equal(data.createdPulls.length, 1);
    assert.equal(data.editedPulls[0]?.text.title, TITLE("v0.3.0", MODULE));
    assert.equal(await requiredOn(BRANCH, `${MODULE}/go.mod`), "v0.3.0");
  });

  test("a module that fails is left out, and the others proposed", async () => {
    await scratch(REQUIRED, undefined, REQUIRED);
    // A go.mod go cannot read.
    repo.write(`${OTHER}/go.mod`, "not a go.mod\n");
    await repo.commit("break other");
    await repo.run("push", "--quiet", "origin", "main");
    const outcome = await proposeRequirements(ctx(), [OTHER, MODULE], "v0.2.0");
    assert.deepEqual(outcome.failed, [OTHER]);
    assert.deepEqual(outcome.raised.map((r) => r.dir), [MODULE]);
    assert.equal(outcome.proposal, Proposal.Opened);
    assert.equal(await requiredOn(BRANCH, `${MODULE}/go.mod`), "v0.2.0");
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
    await proposeRequirements(ctx(), [MODULE], "v0.2.0");
    await git(["merge-base", "--is-ancestor", "main", BRANCH], origin);
  });

  test("nothing to raise", async () => {
    await scratch(REQUIRED);
    assert.equal((await proposeRequirements(ctx(), [MODULE], REQUIRED)).proposal, Proposal.Unchanged, "the same release");
    assert.equal((await proposeRequirements(ctx(), [MODULE], "v0.0.9")).proposal, Proposal.Unchanged, "an older one");
    repo.cleanup();
    rmSync(work, { recursive: true, force: true });
    await scratch(undefined);
    assert.deepEqual(await proposeRequirements(ctx(), [MODULE], "v0.2.0"), { proposal: Proposal.Unchanged, raised: [], failed: [] }, "no requirement");
    assert.deepEqual(data.createdPulls, []);
  });

  test("not a release's tag, or no module", async () => {
    await scratch(REQUIRED);
    for (const tag of ["latest", "v0.2", "0.2.0", "v0.2.0-rc.1"]) {
      await assert.rejects(proposeRequirements(ctx(), [MODULE], tag), Error, tag);
    }
    await assert.rejects(proposeRequirements(ctx(), ["missing"], "v0.2.0"));
    await assert.rejects(proposeRequirements(ctx(), [""], "v0.2.0"));
  });
});

describe("requirementsSummary", () => {
  test("names the pull request and what became of each module", () => {
    const page = requirementsSummary("v0.2.0", ["pgstore", "otelmetric", "redisreplay"], {
      proposal: Proposal.Opened,
      pullRequest: 7,
      raised: [{ dir: "pgstore", from: "v0.1.0" }],
      failed: ["otelmetric"],
    });
    assert.match(page, /^### limes v0\.2\.0 required by the nested modules\n/);
    assert.match(page, /Pull request #7 opened\./);
    assert.match(page, /\| pgstore \| v0\.1\.0 → v0\.2\.0 \|/);
    assert.match(page, /\| otelmetric \| failed/);
    assert.match(page, /\| redisreplay \| left alone/);
  });
  test("with nothing to raise, no pull request", () =>
    assert.match(requirementsSummary("v0.2.0", ["pgstore"], { proposal: Proposal.Unchanged, raised: [], failed: [] }), /No pull request: nothing to raise\./));
});

describe("requirementTitle", () => {
  test("names every module whose go.mod it raises", () => {
    assert.equal(requirementTitle("v0.2.0", ["pgstore"]), `build(${LIMES_REQUIREMENT_SCOPE}): require limes v0.2.0 in pgstore`);
    assert.equal(requirementTitle("v0.2.0", ["pgstore", "otelmetric"]), `build(${LIMES_REQUIREMENT_SCOPE}): require limes v0.2.0 in pgstore and otelmetric`);
    assert.equal(
      requirementTitle("v0.2.0", ["pgstore", "redisreplay", "otelmetric"]),
      `build(${LIMES_REQUIREMENT_SCOPE}): require limes v0.2.0 in pgstore, redisreplay and otelmetric`,
    );
  });
});
