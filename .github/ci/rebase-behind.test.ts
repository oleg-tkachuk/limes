import assert from "node:assert/strict";
import { beforeEach, describe, test } from "node:test";

import { changed, fakeData, fakeGitHub, type FakeData } from "./lib/fake-github.ts";
import type { CheckRun, QueuedPullRequest } from "./lib/github.ts";
import { Outcome, REQUIRED_CHECK, advanceQueue } from "./rebase-behind.ts";

// green is the required check passed at a head.
const green: CheckRun[] = [{ name: REQUIRED_CHECK, conclusion: "SUCCESS", startedAt: undefined }];

// pr is one pull request as the queue lists it, ready and in line unless
// over says otherwise.
function pr(number: number, headSha: string, over: Partial<QueuedPullRequest> = {}): QueuedPullRequest {
  return {
    number,
    open: true,
    draft: false,
    fromFork: false,
    author: { login: "someone", bot: false },
    baseRef: "main",
    headSha,
    changedFiles: 0,
    mergeState: "BEHIND",
    autoMerge: false,
    checks: green,
    ...over,
  };
}

function check(conclusion: string | undefined, startedAt?: string): CheckRun {
  return { name: REQUIRED_CHECK, conclusion, startedAt };
}

describe("advanceQueue", () => {
  let data: FakeData;
  beforeEach(() => {
    data = fakeData();
    data.permissions = { someone: "write", reader: "read", keeper: "admin" };
    data.behindBy = { a1: 1, a2: 1, a3: 1, a4: 1, a5: 1, a6: 1, a7: 1, a8: 1, a9: 1, b0: 0, b1: 1 };
  });

  // rebased is the rebases the run asked for.
  const rebases = () => data.calls.filter((c) => c.startsWith("rebase"));
  const run = () => advanceQueue(fakeGitHub(data));

  test("the first in line, and only it", async () => {
    // Everything ahead of #9 is out of line: a draft, a fork's, a conflict,
    // Renovate's, a reader's, a red one, and one changing CI with no
    // auto-merge. #9 is rebased; #11, behind it, waits. Listed out of order:
    // the lowest number goes first.
    data.files = { a7: changed(".github/workflows/ci.yaml"), a9: changed("verifier.go") };
    data.queue = [
      pr(11, "b1"),
      pr(9, "a9", { changedFiles: 1 }),
      pr(1, "a1", { draft: true }),
      pr(2, "a2", { fromFork: true }),
      pr(3, "a3", { mergeState: "DIRTY" }),
      pr(4, "a4", { author: { login: "renovate", bot: true } }),
      pr(5, "a5", { author: { login: "reader", bot: false } }),
      pr(6, "a6", { checks: [check("FAILURE")] }),
      pr(7, "a7", { changedFiles: 1 }),
    ];
    assert.equal(await run(), Outcome.Rebased);
    assert.deepEqual(rebases(), ["rebase 9 a9"]);
    assert.ok(data.calls.includes("list main 200"));
  });

  test("only the latest run of the required check counts", async () => {
    const superseded = [check("FAILURE", "2026-10-18T18:44:40Z"), check("SUCCESS", "2026-10-18T18:47:44Z")];
    const regressed = [check("SUCCESS", "2026-10-18T18:44:40Z"), check("FAILURE", "2026-10-18T18:47:44Z")];
    data.queue = [pr(1, "a1", { checks: regressed }), pr(2, "a2", { checks: superseded })];
    await run();
    assert.deepEqual(rebases(), ["rebase 2 a2"]);
  });

  test("a cancelled latest run takes it out, a run still going keeps it in", async () => {
    data.queue = [pr(1, "a1", { checks: [check("CANCELLED")] }), pr(2, "a2", { checks: [check(undefined)] })];
    await run();
    assert.deepEqual(rebases(), ["rebase 2 a2"]);
  });

  test("a check that has not run keeps it in", async () => {
    data.queue = [pr(1, "a1", { checks: [] })];
    await run();
    assert.deepEqual(rebases(), ["rebase 1 a1"]);
  });

  // A pull request whose listing may hide a CI definition is one merge-green
  // would leave for a person, so it is not in line.
  test("files left unlisted", async () => {
    data.files = { a1: changed("verifier.go") };
    data.queue = [pr(1, "a1", { changedFiles: 2 }), pr(2, "a2")];
    await run();
    assert.deepEqual(rebases(), ["rebase 2 a2"]);
  });
  test("more files listed than changed", async () => {
    data.files = { a1: changed("a.go", "b.go") };
    data.queue = [pr(1, "a1", { changedFiles: 1 }), pr(2, "a2")];
    await run();
    assert.deepEqual(rebases(), ["rebase 2 a2"]);
  });
  test("a count that is not one", async () => {
    data.files = { a1: changed("verifier.go") };
    data.queue = [pr(1, "a1", { changedFiles: undefined }), pr(2, "a2")];
    await run();
    assert.deepEqual(rebases(), ["rebase 2 a2"]);
  });

  test("a CI change with auto-merge merges itself, so it is in line", async () => {
    data.files = { a7: changed(".github/workflows/ci.yaml") };
    data.queue = [pr(7, "a7", { autoMerge: true, changedFiles: 1 }), pr(9, "a9")];
    await run();
    assert.deepEqual(rebases(), ["rebase 7 a7"]);
  });

  // The files are those of the head the rebase names, not of the head the
  // pull request has now.
  test("the files of the listed head", async () => {
    data.files = { a9: changed("verifier.go") };
    data.queue = [pr(9, "a9", { changedFiles: 1 })];
    await run();
    assert.ok(data.calls.includes("compare main...a9"), data.calls.join("; "));
  });

  test("an up-to-date first holds the queue", async () => {
    data.queue = [pr(10, "b0"), pr(11, "b1")];
    assert.equal(await run(), Outcome.UpToDate);
    assert.deepEqual(rebases(), []);
  });

  test("a first that cannot be rebased gives its place to the next", async () => {
    data.rebaseFails.add(1);
    data.queue = [pr(1, "a1"), pr(2, "a2")];
    assert.equal(await run(), Outcome.Rebased);
    assert.deepEqual(rebases(), ["rebase 1 a1", "rebase 2 a2"]);
  });

  test("a first that cannot be compared gives its place to the next", async () => {
    data.queue = [pr(1, "c9"), pr(2, "a2")];
    await run();
    assert.deepEqual(rebases(), ["rebase 2 a2"]);
  });

  test("nothing open", async () => {
    assert.equal(await run(), Outcome.NoneInLine);
    assert.deepEqual(rebases(), []);
  });
});
