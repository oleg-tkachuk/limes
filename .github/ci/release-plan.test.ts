import assert from "node:assert/strict";
import { copyFileSync, mkdirSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, afterEach, before, beforeEach, describe, test } from "node:test";

import { CHANGELOG, entryVersion, parseEntries } from "./changelog.ts";
import { fakeData, fakeGitHub, type FakeData } from "./lib/fake-github.ts";
import { git } from "./lib/git.ts";
import { LIMES_MODULE } from "./lib/go-modules.ts";
import { releaseCommits, type Module } from "./lib/modules.ts";
import type { Policy } from "./lib/release-policy.ts";
import { tempRepo, type TempRepo } from "./lib/temp-repo.ts";
import { BRANCH, DEPENDENCIES_HEADING, planReleases, proposeRelease, releaseTitle, withDependency, type Planner } from "./release-plan.ts";

const RELEASE = new URL("../release", import.meta.url).pathname;
const REPOSITORY = "example/limes";
const PGSTORE = "pgstore";
const OTELMETRIC = "otelmetric";
const TODAY = "2026-10-31";
// EVERY_COMMIT_DUE releases a module on its first pending commit.
const EVERY_COMMIT_DUE = { minorCommits: 1, patchCommits: 1, immediateTypes: [] };
// NEVER_DUE releases nothing a run by hand does not ask for.
const NEVER_DUE = { minorCommits: 99, patchCommits: 99, immediateTypes: [] };

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

// notes is what semantic-release's dry run writes for version, with one
// feature.
function notes(version: string, feature: string): string {
  return `## [${version}](https://example.com/compare) (${TODAY})\n\n### Features\n\n* ${feature}\n`;
}

describe("the release plan", () => {
  let work: string;
  let origin: string;
  let repo: TempRepo;
  let data: FakeData;
  // dryRuns are the notes each module's dry run writes, by module name.
  let dryRuns: Record<string, string>;
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

  // A checkout of main, its origin a bare repository beside it: limes at
  // v0.1.0, pgstore at pgstore/v0.1.0 requiring it through a replace, and
  // otelmetric, which requires no limes; then one feature of limes.
  beforeEach(async () => {
    work = mkdtempSync(join(tmpdir(), "limes-plan-"));
    origin = join(work, "origin.git");
    await git(["init", "--quiet", "--bare", "--initial-branch=main", origin]);
    repo = await tempRepo();
    await repo.run("remote", "add", "origin", origin);
    mkdirSync(join(repo.dir, ".github/release"), { recursive: true });
    copyFileSync(join(RELEASE, "modules.cjs"), join(repo.dir, ".github/release/modules.cjs"));
    repo.write("go.mod", `module ${LIMES_MODULE}\n\ngo 1.21\n`);
    repo.write("limes.go", "package limes\n");
    repo.write(`${PGSTORE}/go.mod`, `module ${LIMES_MODULE}/${PGSTORE}\n\ngo 1.21\n\nrequire ${LIMES_MODULE} v0.1.0\n\nreplace ${LIMES_MODULE} => ../\n`);
    repo.write(`${PGSTORE}/go.sum`, "");
    repo.write(`${PGSTORE}/pgstore.go`, `package pgstore\n\nimport _ "${LIMES_MODULE}"\n`);
    repo.write(`${OTELMETRIC}/go.mod`, `module ${LIMES_MODULE}/${OTELMETRIC}\n\ngo 1.21\n`);
    await repo.commit("init");
    await repo.run("tag", "v0.1.0");
    await repo.run("tag", `${PGSTORE}/v0.1.0`);
    await repo.run("tag", `${OTELMETRIC}/v0.1.0`);
    repo.write("feature.go", "package limes\n");
    await repo.commit("feat: a feature");
    await repo.run("push", "--quiet", "origin", "main");
    data = fakeData();
    dryRuns = { limes: notes("0.2.0", "a feature") };
  });
  afterEach(() => {
    repo.cleanup();
    rmSync(work, { recursive: true, force: true });
  });

  function planner(policy: Policy = EVERY_COMMIT_DUE, manual = false, only?: string): Planner {
    return {
      root: repo.dir,
      repository: REPOSITORY,
      policy,
      commits: releaseCommits(),
      manual,
      only,
      today: TODAY,
      dryRun: async (module: Module) => dryRuns[module.name],
    };
  }

  test("a due limes raises every nested module that requires it, one with nothing of its own as a patch", async () => {
    const plan = await planReleases(planner());
    assert.deepEqual(
      plan.map((p) => [p.tag, p.raise]),
      [
        ["v0.2.0", false],
        [`${PGSTORE}/v0.1.1`, true],
      ],
    );
    const pgstore = plan[1]?.entry ?? "";
    assert.equal(entryVersion(pgstore), "0.1.1");
    assert.match(pgstore, new RegExp(`compare/${PGSTORE}%2Fv0\\.1\\.0\\.\\.\\.${PGSTORE}%2Fv0\\.1\\.1\\) \\(${TODAY}\\)`));
    assert.match(pgstore, new RegExp(`${DEPENDENCIES_HEADING}\\n\\n\\* require limes v0\\.2\\.0\\n$`));
  });

  test("a nested module with notes of its own keeps them, with the requirement added", async () => {
    dryRuns.pgstore = notes("0.2.0", "a store feature");
    const plan = await planReleases(planner());
    const pgstore = plan.find((p) => p.module.name === PGSTORE);
    assert.equal(pgstore?.tag, `${PGSTORE}/v0.2.0`);
    assert.match(pgstore?.entry ?? "", /\* a store feature\n\n### Dependencies\n\n\* require limes v0\.2\.0\n$/);
  });

  test("a module the policy finds not due is left out, and so is everything it would raise", async () => {
    assert.deepEqual(await planReleases(planner(NEVER_DUE)), []);
  });

  test("a run by hand for one module plans that module alone", async () => {
    dryRuns.pgstore = notes("0.2.0", "a store feature");
    const plan = await planReleases(planner(NEVER_DUE, true, PGSTORE));
    assert.deepEqual(
      plan.map((p) => [p.tag, p.raise]),
      [[`${PGSTORE}/v0.2.0`, false]],
    );
    await assert.rejects(planReleases(planner(NEVER_DUE, true, "nothing")), /no module nothing/);
  });

  test("a major is flagged", async () => {
    dryRuns.limes = "# [1.0.0](https://example.com/compare) (2026-10-31)\n\n* broke\n";
    const plan = await planReleases(planner());
    assert.equal(plan[0]?.tag, "v1.0.0");
    assert.equal(plan[0]?.major, true);
    assert.equal(plan[1]?.major, false);
  });

  // A release nobody asked for waits for the policy's thresholds; the
  // checkout holds one feature since v0.1.0 already.
  describe("by the policy", () => {
    const POLICY = { minorCommits: 3, patchCommits: 5, immediateTypes: ["security"] };
    async function commits(...subjects: string[]): Promise<void> {
      for (const subject of subjects) {
        repo.write("x.go", `// ${subject}\n`);
        await repo.commit(subject);
      }
    }
    const due = async () => (await planReleases(planner(POLICY))).some((p) => p.module.name === "limes");

    test("two features are not due", async () => {
      await commits("feat: b", "docs: c");
      assert.equal(await due(), false);
    });
    test("three features are", async () => {
      await commits("feat: b", "feat: c");
      assert.equal(await due(), true);
    });
    test("four fixes are not due", async () => {
      await commits("fix: a", "fix: b", "perf: c", "fix: d");
      assert.equal(await due(), false);
    });
    test("five fixes are", async () => {
      await commits("fix: a", "fix: b", "perf: c", "fix: d", "fix: e");
      assert.equal(await due(), true);
    });
    test("a security fix is released at once", async () => {
      await commits("security: seal");
      assert.equal(await due(), true);
    });
    test("a breaking change is released at once", async () => {
      await commits("feat!: change");
      assert.equal(await due(), true);
    });
    test("commits before the last release are not counted", async () => {
      await commits("feat: b");
      await repo.run("tag", "v0.1.1");
      await commits("feat: c");
      assert.equal(await due(), false);
    });
    test("a merge commit does not count its pull request twice", async () => {
      await repo.run("checkout", "--quiet", "-b", "topic");
      await commits("feat: b");
      await repo.run("checkout", "--quiet", "main");
      await repo.run("-c", "user.name=t", "-c", "user.email=t@example.invalid", "merge", "--quiet", "--no-ff", "-m", "feat: b again", "topic");
      assert.equal(await due(), false);
    });
    test("a run by hand ignores the thresholds", async () => {
      assert.equal((await planReleases(planner(NEVER_DUE, true))).some((p) => p.module.name === "limes"), true);
    });
  });

  test("withDependency adds to a Dependencies section there is, or adds one last", () => {
    const line = "* require limes v0.2.0";
    assert.equal(withDependency("## [0.1.1](a)\n\n### Bug Fixes\n\n* a fix\n", line), `## [0.1.1](a)\n\n### Bug Fixes\n\n* a fix\n\n${DEPENDENCIES_HEADING}\n\n${line}\n`);
    assert.equal(
      withDependency(`## [0.1.1](a)\n\n${DEPENDENCIES_HEADING}\n\n* bump x\n\n### Bug Fixes\n\n* a fix\n`, line),
      `## [0.1.1](a)\n\n${DEPENDENCIES_HEADING}\n\n${line}\n* bump x\n\n### Bug Fixes\n\n* a fix\n`,
    );
  });

  test("a nested module whose go.mod go cannot read fails the plan", async () => {
    repo.write(`${OTELMETRIC}/go.mod`, "not a go.mod\n");
    await assert.rejects(planReleases(planner()), /go mod edit -json failed/);
  });

  test("releaseTitle names a few tags, and counts more", () => {
    assert.equal(releaseTitle(["v0.2.0", "pgstore/v0.1.1"]), "chore(release): v0.2.0, pgstore/v0.1.1");
    assert.equal(releaseTitle(["v4", "v3", "v2", "v1"]), "chore(release): 4 releases");
  });

  describe("proposeRelease", () => {
    function ctx() {
      return {
        ...planner(),
        github: fakeGitHub(data),
        push: async (branch: string) => void (await repo.run("push", "--quiet", "--force", "origin", `HEAD:refs/heads/${branch}`)),
      };
    }

    test("one commit on its branch raises the requirement and adds each entry, and one pull request names the tags", async () => {
      const proposed = await proposeRelease(ctx());
      assert.equal(proposed?.pullRequest, 101);
      assert.equal(data.createdPulls[0]?.text.draft, false, "a release with no major is ready to merge");
      assert.deepEqual(data.createdPulls.map((p) => [p.head, p.base, p.text.title]), [[BRANCH, "main", releaseTitle(["v0.2.0", `${PGSTORE}/v0.1.1`])]]);
      assert.equal((await git(["rev-list", "--count", `main..${BRANCH}`], origin)).trim(), "1");
      assert.match(await git(["show", `${BRANCH}:${PGSTORE}/go.mod`], origin), new RegExp(`require ${LIMES_MODULE.replaceAll(".", "\\.")} v0\\.2\\.0`));
      assert.deepEqual([...parseEntries(await git(["show", `${BRANCH}:${CHANGELOG}`], origin)).keys()], ["0.2.0"]);
      assert.deepEqual([...parseEntries(await git(["show", `${BRANCH}:${PGSTORE}/${CHANGELOG}`], origin)).keys()], ["0.1.1"]);
      assert.equal(await git(["diff", "--name-only", "main", BRANCH, "--", OTELMETRIC], origin), "");
    });

    test("the pull request still open is updated, and a major is said so", async () => {
      dryRuns.limes = "# [1.0.0](https://example.com/compare) (2026-10-31)\n\n* broke\n";
      data.pullsFrom.set(BRANCH, 7);
      const proposed = await proposeRelease(ctx());
      assert.deepEqual(proposed, { pullRequest: 7, major: true });
      assert.deepEqual(data.createdPulls, []);
      assert.match(data.editedPulls[0]?.text.body ?? "", /cuts a major/);
      assert.equal(data.editedPulls[0]?.text.draft, true, "a major is held as a draft, which merge-green leaves");
    });

    test("nothing due proposes nothing", async () => {
      dryRuns = {};
      assert.equal(await proposeRelease(ctx()), undefined);
      assert.deepEqual(data.createdPulls, []);
    });
  });
});
