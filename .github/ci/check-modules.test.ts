import assert from "node:assert/strict";
import { after, before, describe, test } from "node:test";

import { copyFileSync, mkdirSync } from "node:fs";
import { join } from "node:path";

import { tempRepo, type TempRepo } from "./lib/temp-repo.ts";
import { allPassed, Check, checkModules, checkSummary, eventOf, moduleCheck, type ModuleCheck, Outcome } from "./check-modules.ts";

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
    const results = await checkModules(repo.dir, { name: "push", prBase: "", prHead: "", sha: later }, recording(calls));
    assert.equal(allPassed(results), true);
    assert.deepEqual(calls, [`v0.1.0 ${later} limes`]);
    assert.deepEqual(
      results.map((r) => `${r.module} ${r.outcome}`),
      ["limes passed", "redisreplay skipped", "pgstore skipped", "otelmetric skipped", "exchange skipped", "biscuit skipped"],
    );
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
      ["limes", "redisreplay", "pgstore", "otelmetric", "exchange", "biscuit"].map((m) => `${first} ${later} ${m}`),
    );
  });

  test("one module failing, the others still checked", async () => {
    const calls: string[] = [];
    const results = await checkModules(repo.dir, { name: "pull_request", prBase: first, prHead: later, sha: later }, recording(calls, "limes"));
    assert.equal(allPassed(results), false);
    assert.equal(calls.length, 6);
    assert.deepEqual(results.map((r) => r.outcome), [Outcome.Failed, Outcome.Passed, Outcome.Passed, Outcome.Passed, Outcome.Passed, Outcome.Passed]);
  });

  test("the summary names each check's job and every module's outcome", () => {
    const results = [
      { module: "limes", outcome: Outcome.Failed, message: "limes: Foo | removed" },
      { module: "pgstore", outcome: Outcome.Skipped, message: "no release to check from yet" },
    ];
    const api = checkSummary(Check.Api, results);
    assert.match(api, /^### API compatibility\n/);
    assert.match(api, /\| limes \| failed \| `limes: Foo \\\| removed` \|/);
    assert.match(api, /\| pgstore \| skipped \|/);
    assert.match(checkSummary(Check.Markers, results), /^### Breaking markers\n/);
  });

  test("a message's markup shows as written", () => {
    const page = checkSummary(Check.Markers, [{ module: "limes", outcome: Outcome.Failed, message: "feat!: [x](https://example.com) ![](https://example.com/p.png)" }]);
    assert.match(page, /\| `feat!: \[x\]\(https:\/\/example\.com\) !\[\]\(https:\/\/example\.com\/p\.png\)` \|/);
  });

  test("an unknown check is refused", () => {
    assert.throws(() => moduleCheck("bogus", repo.dir, undefined, undefined), /usage/);
    assert.doesNotThrow(() => moduleCheck(Check.Markers, repo.dir, undefined, undefined));
  });
});

describe("eventOf", () => {
  const required = (vars: Record<string, string>) => (name: string) => {
    const v = vars[name];
    if (v === undefined) {
      throw new Error(`${name} is not set`);
    }
    return v;
  };
  test("a pull request's event names its base and head", () => {
    const vars = { EVENT: "pull_request", SHA: "abc", PR_BASE: "base", PR_HEAD: "head" };
    assert.deepEqual(eventOf(required(vars), (n) => vars[n as keyof typeof vars]), { name: "pull_request", prBase: "base", prHead: "head", sha: "abc" });
  });
  test("a push's names none", () => {
    const vars = { EVENT: "push", SHA: "abc" };
    assert.deepEqual(eventOf(required(vars), () => undefined), { name: "push", prBase: "", prHead: "", sha: "abc" });
  });
  test("one without a sha is refused", () => {
    assert.throws(() => eventOf(required({ EVENT: "push" }), () => undefined), /SHA is not set/);
  });
});
