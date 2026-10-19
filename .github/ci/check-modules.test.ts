import assert from "node:assert/strict";
import { after, before, describe, test } from "node:test";

import { copyFileSync, mkdirSync } from "node:fs";
import { join } from "node:path";

import { tempRepo, type TempRepo } from "./lib/temp-repo.ts";
import { Check, checkModules, moduleCheck, type ModuleCheck } from "./check-modules.ts";

const RELEASE = new URL("../release/", import.meta.url).pathname;

describe("checkModules", () => {
  let repo: TempRepo;
  let first: string;
  let later: string;
  before(async () => {
    repo = await tempRepo();
    mkdirSync(join(repo.dir, ".github/release"), { recursive: true });
    copyFileSync(join(RELEASE, "modules.cjs"), join(repo.dir, ".github/release/modules.cjs"));
    first = await repo.commit("feat: start");
    await repo.run("tag", "v0.1.0");
    later = await repo.commit("fix: later");
  });
  after(() => repo.cleanup());

  // recording is a check that logs each call and fails for failing.
  function recording(calls: string[], failing?: string): ModuleCheck {
    return async (module, base, head) => {
      calls.push(`${base} ${head} ${module.name}`);
      return { ok: module.name !== failing, message: module.name };
    };
  }

  test("a push checks each module from its own tag, skipping one never released", async () => {
    const calls: string[] = [];
    assert.equal(await checkModules(repo.dir, { name: "push", prBase: "", prHead: "", sha: later }, recording(calls)), true);
    assert.deepEqual(calls, [`v0.1.0 ${later} limes`]);
    await repo.run("tag", "redisreplay/v0.1.0", first);
    calls.length = 0;
    await checkModules(repo.dir, { name: "push", prBase: "", prHead: "", sha: later }, recording(calls));
    assert.deepEqual(calls, [`v0.1.0 ${later} limes`, `redisreplay/v0.1.0 ${later} redisreplay`]);
  });

  test("a pull request checks every module of the table, in its order", async () => {
    const calls: string[] = [];
    await checkModules(repo.dir, { name: "pull_request", prBase: first, prHead: later, sha: later }, recording(calls));
    assert.deepEqual(
      calls,
      ["limes", "redisreplay", "pgstore", "otelmetric"].map((m) => `${first} ${later} ${m}`),
    );
  });

  test("one module failing, the others still checked", async () => {
    const calls: string[] = [];
    assert.equal(await checkModules(repo.dir, { name: "pull_request", prBase: first, prHead: later, sha: later }, recording(calls, "limes")), false);
    assert.equal(calls.length, 4);
  });

  test("an unknown check is refused", () => {
    assert.throws(() => moduleCheck("bogus", repo.dir, undefined, undefined), /usage/);
    assert.doesNotThrow(() => moduleCheck(Check.Markers, repo.dir, undefined, undefined));
  });
});
