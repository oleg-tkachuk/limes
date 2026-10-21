import assert from "node:assert/strict";
import { beforeEach, describe, test } from "node:test";

import { changed, fakeData, fakeGitHub, prRun, type FakeData } from "./lib/fake-github.ts";
import type { PullRequest } from "./lib/github.ts";
import { RED_ISSUE, State, reportHealth, stateOf } from "./main-health.ts";
import { mergeGreen, mergeWaiting } from "./merge-green.ts";

const SHA = "0123456789abcdef0123456789abcdef01234567";
const MAIN = "1111111111111111111111111111111111111111";
const red = { id: 1, conclusion: "failure", headSha: MAIN, url: "https://example.invalid/run/1" };
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
    data.prRuns[SHA] = [prRun(7)];
  });

  test("holds a green pull request", async () => {
    data.mainRun = red;
    const [decision] = await mergeGreen(fakeGitHub(data), SHA, JSON.stringify([{ number: 7 }]));
    assert.equal(decision?.merged, false);
    assert.match(decision?.note ?? "", /main is red/);
    assert.ok(!data.calls.some((c) => c.startsWith("merge")));
  });

  test("a run on main red only outside its required check holds nothing", async () => {
    data.mainRun = red;
    data.jobs.set(red.id, [
      { name: "All checks passed", status: "completed", conclusion: "success" },
      { name: "Released limes (pgstore)", status: "completed", conclusion: "failure" },
    ]);
    const [decision] = await mergeGreen(fakeGitHub(data), SHA, JSON.stringify([{ number: 7 }]));
    assert.equal(decision?.merged, true);
  });

  test("a red required check holds it", async () => {
    data.mainRun = red;
    data.jobs.set(red.id, [{ name: "All checks passed", status: "completed", conclusion: "failure" }]);
    const [decision] = await mergeGreen(fakeGitHub(data), SHA, JSON.stringify([{ number: 7 }]));
    assert.equal(decision?.merged, false);
  });

  test("a cancelled run on main holds nothing", async () => {
    data.mainRun = { ...red, conclusion: "cancelled" };
    const [decision] = await mergeGreen(fakeGitHub(data), SHA, JSON.stringify([{ number: 7 }]));
    assert.equal(decision?.merged, true);
  });

  test("once green, what waited is merged, and only what its own run passed", async () => {
    data.mainRun = green;
    const OTHER = "2222222222222222222222222222222222222222";
    const THIRD = "3333333333333333333333333333333333333333";
    data.pulls.set(8, pull(8, OTHER));
    data.pulls.set(9, pull(9, THIRD));
    data.behindBy[OTHER] = 0;
    data.behindBy[THIRD] = 0;
    data.files[OTHER] = changed("verifier.go");
    data.files[THIRD] = changed("verifier.go");
    data.open = [
      { number: 7, headSha: SHA },
      { number: 8, headSha: OTHER },
      { number: 9, headSha: THIRD },
    ];
    // #8's own run failed; #9's never ran into main, though a run of another
    // pull request into another base passed at its head.
    data.prRuns = { [SHA]: [prRun(7)], [OTHER]: [prRun(8, "failure")], [THIRD]: [prRun(10, "success", "release/v1")] };
    const decisions = await mergeWaiting(fakeGitHub(data));
    assert.deepEqual(decisions.map((d) => [d.number, d.merged]), [[7, true]]);
  });

  test("while still red, nothing that waited is merged", async () => {
    data.mainRun = red;
    data.open = [{ number: 7, headSha: SHA }];
    data.prRuns = { [SHA]: [prRun(7)] };
    assert.deepEqual(await mergeWaiting(fakeGitHub(data)), []);
  });
});
