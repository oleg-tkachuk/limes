import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { beforeEach, describe, test } from "node:test";

import { parse } from "yaml";

import { changed, fakeData, fakeGitHub, type FakeData } from "./lib/fake-github.ts";
import type { PullRequest } from "./lib/github.ts";
import { RED_ISSUE, REQUIRED_CHECK, State, reportHealth, stateOf } from "./main-health.ts";
import { CI_WORKFLOW } from "./lib/runs.ts";
import { mergeGreen, mergeWaiting } from "./merge-green.ts";

const SHA = "0123456789abcdef0123456789abcdef01234567";
const MAIN = "1111111111111111111111111111111111111111";
const red = { conclusion: "failure", headSha: MAIN, url: "https://example.invalid/run/1" };
const green = { ...red, conclusion: "success" };

function pull(number: number, headSha: string): PullRequest {
  return {
    number,
    open: true,
    draft: false,
    fromFork: false,
    author: { login: "someone", bot: false },
    baseRef: "main",
    headSha,
    changedFiles: 1,
  };
}

describe("stateOf", () => {
  for (const [conclusion, want] of [
    ["success", State.Green],
    ["failure", State.Red],
    ["timed_out", State.Red],
    ["cancelled", State.Unknown],
    ["skipped", State.Unknown],
  ] as const) {
    test(conclusion, () => assert.equal(stateOf(conclusion), want));
  }
});

describe("reportHealth", () => {
  let data: FakeData;
  beforeEach(() => {
    data = fakeData();
  });
  test("a red main opens one issue, then comments on it", async () => {
    await reportHealth(fakeGitHub(data), red);
    await reportHealth(fakeGitHub(data), { ...red, headSha: SHA });
    assert.equal(data.issues.size, 1);
    const [issue] = [...data.issues.values()];
    assert.equal(issue?.title, RED_ISSUE);
    assert.equal(issue?.comments.length, 1);
  });
  test("a green main closes it", async () => {
    await reportHealth(fakeGitHub(data), red);
    assert.equal(await reportHealth(fakeGitHub(data), green), State.Green);
    assert.equal(data.issues.size, 0);
    assert.equal(data.closed.length, 1);
  });
  test("a cancelled run says nothing", async () => {
    await reportHealth(fakeGitHub(data), red);
    await reportHealth(fakeGitHub(data), { ...red, conclusion: "cancelled" });
    assert.equal(data.issues.size, 1);
    assert.equal([...data.issues.values()][0]?.comments.length, 0);
  });
});

describe("a red main", () => {
  let data: FakeData;
  beforeEach(() => {
    data = fakeData();
    data.permissions = { someone: "write" };
    data.behindBy[SHA] = 0;
    data.files[SHA] = changed("verifier.go");
    data.pulls.set(7, pull(7, SHA));
  });

  test("holds a green pull request", async () => {
    data.mainRun = red;
    const [decision] = await mergeGreen(fakeGitHub(data), SHA, JSON.stringify([{ number: 7 }]));
    assert.equal(decision?.merged, false);
    assert.match(decision?.note ?? "", /main is red/);
    assert.ok(!data.calls.some((c) => c.startsWith("merge")));
  });

  test("a cancelled run on main holds nothing", async () => {
    data.mainRun = { ...red, conclusion: "cancelled" };
    const [decision] = await mergeGreen(fakeGitHub(data), SHA, JSON.stringify([{ number: 7 }]));
    assert.equal(decision?.merged, true);
  });

  test("once green, what waited is merged, and only what passed its check", async () => {
    data.mainRun = green;
    const OTHER = "2222222222222222222222222222222222222222";
    data.pulls.set(8, pull(8, OTHER));
    data.open = [
      { number: 7, headSha: SHA },
      { number: 8, headSha: OTHER },
    ];
    data.checks = { [SHA]: "success", [OTHER]: "failure" };
    const decisions = await mergeWaiting(fakeGitHub(data), REQUIRED_CHECK);
    assert.deepEqual(decisions.map((d) => [d.number, d.merged]), [[7, true]]);
  });

  test("while still red, nothing that waited is merged", async () => {
    data.mainRun = red;
    data.open = [{ number: 7, headSha: SHA }];
    data.checks = { [SHA]: "success" };
    assert.deepEqual(await mergeWaiting(fakeGitHub(data), REQUIRED_CHECK), []);
  });
});

// The check main-health waits on is the one ci.yaml's required job is named,
// and the workflow it reads is ci.yaml.
test("the required check is ci.yaml's", () => {
  const ci = parse(readFileSync(new URL(`../../${CI_WORKFLOW}`, import.meta.url), "utf8")) as { jobs: Record<string, { name?: string }> };
  assert.equal(ci.jobs.checks?.name, REQUIRED_CHECK);
});
