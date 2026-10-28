import assert from "node:assert/strict";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, before, describe, test } from "node:test";

import { git } from "./lib/git.ts";
import { tempRepo, type TempRepo } from "./lib/temp-repo.ts";
import { catchUp, Command, releasable, releaseHeadArgs } from "./release-head.ts";

describe("releasable", () => {
  let repo: TempRepo;
  let passed: string;
  let elsewhere: string;
  before(async () => {
    repo = await tempRepo();
    passed = await repo.commit("one");
  });
  after(() => repo.cleanup());

  test("the head CI passed", async () => assert.equal(await releasable(passed, repo.dir), true));
  test("the head by its short sha", async () => assert.equal(await releasable(passed.slice(0, 8), repo.dir), true));
  test("an empty sha is refused", async () => assert.rejects(releasable("", repo.dir), /no dispatched sha/));

  test("main moved past it with documentation only", async () => {
    repo.write("docs/guide.md", "note\n");
    await repo.commit("docs: a note");
    // The head's code is the passed commit's, and no CI run would dispatch a
    // release for it.
    assert.equal(await releasable(passed, repo.dir), true);
  });
  test("main moved past it with code", async () => {
    repo.write("m.go", "package m\n");
    await repo.commit("feat: code");
    assert.equal(await releasable(passed, repo.dir), false);
  });
  test("a sha this clone does not have", async () => assert.equal(await releasable("0".repeat(40), repo.dir), false));
  // A sha not behind the head is never released in its place, even when the
  // two differ by documentation only: only the ancestry check refuses it.
  test("a sha off the head's history", async () => {
    await repo.run("checkout", "--quiet", "-b", "side");
    repo.write("docs/other.md", "other\n");
    elsewhere = await repo.commit("docs: elsewhere");
    await repo.run("checkout", "--quiet", "-");
    repo.write("docs/guide.md", "more\n");
    await repo.commit("docs: more");
    assert.equal(await releasable(elsewhere, repo.dir), false);
  });
});

describe("catchUp", () => {
  let work: string;
  let pusher: TempRepo;
  let release: string;
  let dispatched: string;
  before(async () => {
    work = mkdtempSync(join(tmpdir(), "limes-ci-"));
    const origin = join(work, "origin.git");
    await git(["init", "--quiet", "--bare", "--initial-branch=main", origin]);
    pusher = await tempRepo();
    await pusher.run("remote", "add", "origin", origin);
    pusher.write("m.go", "package m\n");
    await pusher.commit("feat: code");
    await pusher.run("push", "--quiet", "origin", "main");
    release = join(work, "release");
    await git(["clone", "--quiet", origin, release]);
    dispatched = (await git(["rev-parse", "HEAD"], release)).trim();
  });
  after(() => {
    pusher.cleanup();
    rmSync(work, { recursive: true, force: true });
  });

  const decide = async () => {
    await catchUp(release);
    return releasable(dispatched, release);
  };

  test("main where the checkout left it", async () => assert.equal(await decide(), true));
  test("documentation landed after the checkout", async () => {
    pusher.write("docs/guide.md", "note\n");
    const head = await pusher.commit("docs: a note");
    await pusher.run("push", "--quiet", "origin", "main");
    assert.equal(await decide(), true);
    assert.equal((await git(["rev-parse", "HEAD"], release)).trim(), head, "the checkout was not brought up to main");
  });
  test("code landed after the checkout", async () => {
    pusher.write("m.go", "package m // more\n");
    await pusher.commit("feat: more code");
    await pusher.run("push", "--quiet", "origin", "main");
    assert.equal(await decide(), false);
  });
});

describe("releaseHeadArgs", () => {
  test("a check of a dispatched sha", () => assert.deepEqual(releaseHeadArgs([Command.Check, "abc"]), { command: Command.Check, sha: "abc" }));
  test("a catch-up names no sha", () => assert.deepEqual(releaseHeadArgs([Command.CatchUp]), { command: Command.CatchUp, sha: "" }));
  for (const args of [[], ["release"]]) {
    test(`refuses ${JSON.stringify(args)}`, () => assert.throws(() => releaseHeadArgs(args), /usage: release-head\.ts/));
  }
});
