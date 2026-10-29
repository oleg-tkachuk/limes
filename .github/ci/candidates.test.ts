import assert from "node:assert/strict";
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, afterEach, before, beforeEach, describe, test } from "node:test";

import { BRANCH, CANDIDATE_HEADING, NO_RELEASE, candidatesTable, checkTable, guideTable, proposeRow, rowTitle, withTable, writeTable, type Context } from "./candidates.ts";
import { fakeData, fakeGitHub, type FakeData } from "./lib/fake-github.ts";
import { git } from "./lib/git.ts";
import { releasedModules } from "./lib/modules.ts";
import { tempRepo, type TempRepo } from "./lib/temp-repo.ts";
import { MIGRATION_GUIDE } from "./release-checks.ts";

const RELEASE = new URL("../release", import.meta.url).pathname;
const PGSTORE = "pgstore";

// GIT_IDENTITY lets propose commit with no identity of the machine's.
const GIT_IDENTITY: Record<string, string> = {
  GIT_AUTHOR_NAME: "test",
  GIT_AUTHOR_EMAIL: "test@example.com",
  GIT_COMMITTER_NAME: "test",
  GIT_COMMITTER_EMAIL: "test@example.com",
};

// guide is a migration guide around table.
function guide(table: string): string {
  return `# Migrating\n\nIntro.\n\n${table}\n\n## v0.2.0\n\n- an entry\n`;
}

// row is a table line of cells.
function row(...cells: string[]): string {
  return `| ${cells.join(" | ")} |`;
}

describe("the table of candidates", () => {
  let repo: TempRepo;
  let modules: string[];

  beforeEach(async () => {
    repo = await tempRepo();
    mkdirSync(join(repo.dir, ".github/release"), { recursive: true });
    copyFileSync(join(RELEASE, "modules.cjs"), join(repo.dir, ".github/release/modules.cjs"));
    modules = releasedModules(repo.dir);
    repo.write("go.mod", "module example.com/limes\n");
    repo.write("limes.go", "package limes\n");
    repo.write(`${PGSTORE}/go.mod`, "module example.com/limes/pgstore\n");
    repo.write(`${PGSTORE}/store.go`, "package pgstore\n");
    repo.write(MIGRATION_GUIDE, guide(row(CANDIDATE_HEADING, "limes")));
    await repo.commit("init");
    await repo.run("tag", "v0.1.0");
    await repo.run("tag", `${PGSTORE}/v0.1.0`);
  });
  afterEach(() => repo.cleanup());

  // cells is a row of the table: the candidate, then limes's cell, then
  // each nested module's, NO_RELEASE where none is given.
  function cells(candidate: string, limes: string, nested: Record<string, string> = {}): string {
    return row(candidate, limes, ...modules.slice(1).map((m) => nested[m] ?? NO_RELEASE));
  }

  test("a candidate at a release carries it, with each module's release on main", async () => {
    await repo.run("tag", "v1.0.0-rc.1");
    const table = (await candidatesTable(repo.dir, "HEAD")).split("\n");
    assert.equal(table[0], row(CANDIDATE_HEADING, ...modules));
    assert.equal(table[2], cells("`rc.1`", "`v0.1.0`", { [PGSTORE]: "`v0.1.0`" }));
    assert.equal(table.length, 3);
  });

  test("a candidate past a release's Go code is marked between releases", async () => {
    repo.write("more.go", "package limes\n");
    await repo.commit("feat: more");
    await repo.run("tag", "v1.0.0-rc.1");
    assert.equal((await candidatesTable(repo.dir, "HEAD")).split("\n")[2], cells("`rc.1`", "`v0.1.0`+", { [PGSTORE]: "`v0.1.0`" }));
  });

  test("a change to a nested module or to no Go file leaves the candidate at the release", async () => {
    repo.write(`${PGSTORE}/more.go`, "package pgstore\n");
    repo.write("README.md", "readme\n");
    await repo.commit("feat(pgstore): more");
    await repo.run("tag", `${PGSTORE}/v0.2.0`);
    await repo.run("tag", "v1.0.0-rc.1");
    assert.equal((await candidatesTable(repo.dir, "HEAD")).split("\n")[2], cells("`rc.1`", "`v0.1.0`", { [PGSTORE]: "`v0.2.0`" }));
  });

  test("candidates come newest first, by number, and only those reachable", async () => {
    for (const n of [1, 2, 10]) {
      repo.write(`f${n}.go`, "package limes\n");
      await repo.commit(`fix: ${n}`);
      await repo.run("tag", `v1.0.0-rc.${n}`);
    }
    await repo.run("switch", "--quiet", "-c", "side", "HEAD~1");
    const onSide = (await candidatesTable(repo.dir, "HEAD")).split("\n").slice(2).map((l) => l.split(" | ")[0]);
    assert.deepEqual(onSide, ["| `rc.2`", "| `rc.1`"]);
    await repo.run("switch", "--quiet", "main");
    const onMain = (await candidatesTable(repo.dir, "HEAD")).split("\n").slice(2).map((l) => l.split(" | ")[0]);
    assert.deepEqual(onMain, ["| `rc.10`", "| `rc.2`", "| `rc.1`"]);
  });

  test("check passes the tags' table, refuses another, and write makes it the tags'", async () => {
    await repo.run("tag", "v1.0.0-rc.1");
    await assert.rejects(checkTable(repo.dir, "HEAD"), /is not the tags'.*candidates\.ts write/);
    assert.equal(await writeTable(repo.dir, "HEAD"), true);
    assert.match(await checkTable(repo.dir, "HEAD"), /holds every candidate/);
    assert.equal(await writeTable(repo.dir, "HEAD"), false);
    const written = readFileSync(join(repo.dir, MIGRATION_GUIDE), "utf8");
    assert.equal(written, guide(await candidatesTable(repo.dir, "HEAD")));
  });

  test("a guide with no table is refused", async () => {
    repo.write(MIGRATION_GUIDE, "# Migrating\n");
    await repo.commit("docs: drop the table");
    await assert.rejects(checkTable(repo.dir, "HEAD"), /has no table of candidates/);
  });
});

test("withTable replaces the table and nothing around it", () => {
  const old = `${row(CANDIDATE_HEADING, "limes")}\n${row("---", "---")}\n${row("`rc.1`", "`v0.1.0`")}`;
  const table = `${row(CANDIDATE_HEADING, "limes")}\n${row("---", "---")}\n${row("`rc.2`", "`v0.2.0`")}\n${row("`rc.1`", "`v0.1.0`")}`;
  assert.equal(withTable(guide(old), table), guide(table));
  assert.equal(guideTable(guide(table)), table);
});

describe("proposeRow", () => {
  let work: string;
  let repo: TempRepo;
  let data: FakeData;
  const saved: Record<string, string | undefined> = {};

  before(() => {
    for (const [k, v] of Object.entries(GIT_IDENTITY)) {
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

  // A checkout of main whose origin is a bare repository beside it, with a
  // release and a candidate of it tagged on the remote alone.
  beforeEach(async () => {
    work = mkdtempSync(join(tmpdir(), "limes-candidates-"));
    const origin = join(work, "origin.git");
    await git(["init", "--quiet", "--bare", "--initial-branch=main", origin]);
    repo = await tempRepo();
    await repo.run("remote", "add", "origin", origin);
    mkdirSync(join(repo.dir, ".github/release"), { recursive: true });
    copyFileSync(join(RELEASE, "modules.cjs"), join(repo.dir, ".github/release/modules.cjs"));
    repo.write("limes.go", "package limes\n");
    repo.write(MIGRATION_GUIDE, guide(row(CANDIDATE_HEADING, "limes")));
    await repo.commit("init");
    await repo.run("tag", "v0.1.0");
    await repo.run("tag", "v1.0.0-rc.1");
    await repo.run("push", "--quiet", "origin", "main", "v0.1.0", "v1.0.0-rc.1");
    await repo.run("tag", "--delete", "v1.0.0-rc.1");
    data = fakeData();
  });
  afterEach(() => {
    repo.cleanup();
    rmSync(work, { recursive: true, force: true });
  });

  function ctx(): Context {
    return { github: fakeGitHub(data), root: repo.dir, push: async (branch) => void (await repo.run("push", "--quiet", "--force", "origin", `HEAD:refs/heads/${branch}`)) };
  }

  test("commits the table with the candidate's row on its branch and opens a pull request", async () => {
    assert.equal(await proposeRow(ctx(), "v1.0.0-rc.1"), 101);
    assert.deepEqual(data.createdPulls.map((p) => [p.head, p.base, p.text.title]), [[BRANCH, "main", rowTitle("v1.0.0-rc.1")]]);
    assert.equal((await repo.run("log", "-1", "--format=%s", `origin/${BRANCH}`)).trim(), rowTitle("v1.0.0-rc.1"));
    const table = guideTable(await repo.run("show", `origin/${BRANCH}:${MIGRATION_GUIDE}`));
    assert.match(table, /\| `rc\.1` \| `v0\.1\.0` \|/);
  });

  test("updates the pull request still open", async () => {
    data.pullsFrom.set(BRANCH, 7);
    assert.equal(await proposeRow(ctx(), "v1.0.0-rc.1"), 7);
    assert.deepEqual(data.createdPulls, []);
    assert.deepEqual(data.editedPulls.map((p) => p.number), [7]);
  });

  test("proposes nothing when main lists the candidate already", async () => {
    await repo.run("fetch", "--quiet", "--tags", "origin");
    await writeTable(repo.dir, "HEAD");
    await repo.commit("docs: the row");
    await repo.run("push", "--quiet", "origin", "main");
    assert.equal(await proposeRow(ctx(), "v1.0.0-rc.1"), undefined);
    assert.deepEqual(data.createdPulls, []);
  });

  test("refuses a tag that is no candidate of 1.0", async () => {
    await assert.rejects(proposeRow(ctx(), "v0.2.0-rc.1"), /not a candidate/);
  });
});
