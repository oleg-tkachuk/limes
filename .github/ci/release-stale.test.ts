import assert from "node:assert/strict";
import { copyFileSync, mkdirSync } from "node:fs";
import { join } from "node:path";
import { afterEach, beforeEach, describe, test } from "node:test";

import { CHANGELOG, TITLE } from "./changelog.ts";
import { fakeData, fakeGitHub, type FakeData } from "./lib/fake-github.ts";
import { releaseCommits } from "./lib/modules.ts";
import type { Policy } from "./lib/release-policy.ts";
import { RELEASE_BRANCH } from "./lib/stale-plan.ts";
import { tempRepo, type TempRepo } from "./lib/temp-repo.ts";
import { checkReleasePlan, type Checker } from "./release-stale.ts";

const RELEASE = new URL("../release", import.meta.url).pathname;
const PGSTORE = "pgstore";
const OTELMETRIC = "otelmetric";
// PULL is the open release pull request's number.
const PULL = 5;
// NEVER_DUE makes no module due; EVERY_COMMIT_DUE, any on its first
// pending commit.
const NEVER_DUE: Policy = { minorCommits: 99, patchCommits: 99, immediateTypes: [] };
const EVERY_COMMIT_DUE: Policy = { minorCommits: 1, patchCommits: 1, immediateTypes: [] };
// MISSING_SHA is a commit no clone holds.
const MISSING_SHA = "0123456789abcdef0123456789abcdef01234567";

// changelog is a changelog of entries, newest first.
function changelog(...entries: string[]): string {
  return `${TITLE}\n\nIntro.\n\n${entries.join("\n\n")}\n`;
}

describe("checkReleasePlan", () => {
  let repo: TempRepo;
  let data: FakeData;
  // release is the release commit, the head of the open pull request.
  let release: string;

  // main with limes and pgstore at v0.1.0; a release commit on
  // RELEASE_BRANCH cut from it, planning v0.2.0 and pgstore/v0.1.1, open as
  // a ready pull request; main still at the plan's base.
  beforeEach(async () => {
    repo = await tempRepo();
    mkdirSync(join(repo.dir, ".github/release"), { recursive: true });
    copyFileSync(join(RELEASE, "modules.cjs"), join(repo.dir, ".github/release/modules.cjs"));
    repo.write("go.mod", "module example.com/limes\n");
    repo.write(`${PGSTORE}/go.mod`, "module example.com/limes/pgstore\n");
    repo.write(CHANGELOG, changelog("## [0.1.0](a)"));
    repo.write(`${PGSTORE}/${CHANGELOG}`, changelog("## [0.1.0](b)"));
    await repo.commit("init");
    await repo.run("tag", "v0.1.0");
    await repo.run("tag", `${PGSTORE}/v0.1.0`);
    await repo.run("switch", "--quiet", "-c", RELEASE_BRANCH);
    repo.write(CHANGELOG, changelog("## [0.2.0](c)", "## [0.1.0](a)"));
    repo.write(`${PGSTORE}/${CHANGELOG}`, changelog("## [0.1.1](d)", "## [0.1.0](b)"));
    release = await repo.commit("chore(release): v0.2.0, pgstore/v0.1.1");
    await repo.run("switch", "--quiet", "main");
    data = fakeData();
    data.pullsFrom.set(RELEASE_BRANCH, PULL);
    data.pulls.set(PULL, {
      number: PULL,
      open: true,
      draft: false,
      fromFork: false,
      author: { login: "maintainer", bot: false },
      baseRef: "main",
      headSha: release,
      changedFiles: 2,
    });
    data.issues.set(PULL, { title: "chore(release): v0.2.0, pgstore/v0.1.1", body: "", comments: [] });
  });
  afterEach(() => repo.cleanup());

  async function checker(policy: Policy = NEVER_DUE): Promise<Checker> {
    return {
      github: fakeGitHub(data),
      root: repo.dir,
      head: (await repo.run("rev-parse", "HEAD")).trim(),
      commits: releaseCommits(),
      policy,
      fetch: async () => {},
    };
  }

  const comments = () => data.issues.get(PULL)?.comments ?? [];

  test("a commit after the plan's base that releases a module of it makes the pull request a draft, and says which", async () => {
    repo.write(`${PGSTORE}/break.go`, "package pgstore\n");
    await repo.commit(`fix(${PGSTORE})!: a break after the plan`);
    const verdict = await checkReleasePlan(await checker());
    assert.equal(verdict.drafted, true, verdict.reason);
    assert.deepEqual(data.drafted, [{ number: PULL, draft: true }]);
    assert.deepEqual(
      verdict.staled.map((s) => [s.module, s.planned, s.commits.map((c) => c.subject)]),
      [[PGSTORE, true, [`fix(${PGSTORE})!: a break after the plan`]]],
    );
    assert.equal(comments().length, 1);
    assert.match(comments()[0] ?? "", /`fix\(pgstore\)!: a break after the plan` — releases pgstore \(minor\), which the plan releases/);
  });

  test("a commit that makes a module the plan leaves out due stales it too", async () => {
    repo.write(`${OTELMETRIC}/fix.go`, "package otelmetric\n");
    await repo.commit(`fix(${OTELMETRIC}): a fix after the plan`);
    const verdict = await checkReleasePlan(await checker(EVERY_COMMIT_DUE));
    assert.equal(verdict.drafted, true, verdict.reason);
    assert.deepEqual(verdict.staled.map((s) => [s.module, s.planned]), [[OTELMETRIC, false]]);
  });

  const fresh: { name: string; changes: [string, string][]; policy?: Policy }[] = [
    { name: "main still at the plan's base", changes: [] },
    { name: "documentation and chores since", changes: [["README.md", "docs: a word"], ["tools/x.txt", "chore: a chore"], [`${PGSTORE}/README.md`, `docs(${PGSTORE}): a word`]] },
    { name: "a commit of a module the plan leaves out, short of due", changes: [[`${OTELMETRIC}/fix.go`, `fix(${OTELMETRIC}): a fix`]] },
  ];
  for (const c of fresh) {
    test(`a plan is current with ${c.name}: left ready`, async () => {
      for (const [path, subject] of c.changes) {
        repo.write(path, `${subject}\n`);
        await repo.commit(subject);
      }
      const verdict = await checkReleasePlan(await checker(c.policy));
      assert.equal(verdict.drafted, false);
      assert.deepEqual(verdict.staled, []);
      assert.match(verdict.reason, /is current/);
      assert.deepEqual(data.drafted, []);
      assert.deepEqual(comments(), []);
    });
  }

  test("a draft already is not commented on again", async () => {
    const pr = data.pulls.get(PULL);
    assert.ok(pr !== undefined);
    data.pulls.set(PULL, { ...pr, draft: true });
    repo.write("feature.go", "package limes\n");
    await repo.commit("feat: a feature after the plan");
    const verdict = await checkReleasePlan(await checker());
    assert.equal(verdict.drafted, false);
    assert.equal(verdict.staled.length, 1);
    assert.match(verdict.reason, /a draft already/);
    assert.deepEqual(data.drafted, []);
    assert.deepEqual(comments(), []);
  });

  test("no release pull request open: nothing to judge", async () => {
    data.pullsFrom.clear();
    const verdict = await checkReleasePlan(await checker());
    assert.equal(verdict.pullRequest, undefined);
    assert.match(verdict.reason, /no release pull request/);
  });

  test("a head that cannot be fetched is not judged", async () => {
    const pr = data.pulls.get(PULL);
    assert.ok(pr !== undefined);
    data.pulls.set(PULL, { ...pr, headSha: MISSING_SHA });
    const fetched: string[] = [];
    const verdict = await checkReleasePlan({ ...(await checker()), fetch: async (sha) => void fetched.push(sha) });
    assert.deepEqual(fetched, [MISSING_SHA]);
    assert.match(verdict.reason, /not in this clone/);
    assert.deepEqual(data.drafted, []);
  });

  test("a branch of more than the release commit is not judged", async () => {
    await repo.run("switch", "--quiet", RELEASE_BRANCH);
    const second = await repo.commit("chore: one more");
    await repo.run("switch", "--quiet", "main");
    const pr = data.pulls.get(PULL);
    assert.ok(pr !== undefined);
    data.pulls.set(PULL, { ...pr, headSha: second });
    repo.write("feature.go", "package limes\n");
    await repo.commit("feat: a feature after the plan");
    const verdict = await checkReleasePlan(await checker());
    assert.match(verdict.reason, /is not in the history of .*cannot be judged/);
    assert.deepEqual(data.drafted, []);
  });
});
