import assert from "node:assert/strict";
import { beforeEach, describe, test } from "node:test";

import type { PullRequest } from "./lib/github.ts";
import { PULL_REQUEST_TITLE, RUNNING, changed, fakeData, fakeGitHub, prRun, type FakeData } from "./lib/fake-github.ts";
import { mergeGreen, pullRequestNumbers, unpassed } from "./merge-green.ts";

const SHA = "0123456789abcdef0123456789abcdef01234567";
const OTHER_SHA = "fedcba9876543210fedcba9876543210fedcba98";
const PR = 7;
const ONE_PR = JSON.stringify([{ number: PR }]);

// pull is a pull request ready to merge — open, ready, same-repository, by a
// writer, into main, at the tested commit — with what the case changes.
function pull(over: Partial<PullRequest> = {}): PullRequest {
  return {
    number: PR,
    open: true,
    draft: false,
    fromFork: false,
    author: { login: "someone", bot: false },
    baseRef: "main",
    headSha: SHA,
    changedFiles: undefined,
    ...over,
  };
}

describe("mergeGreen", () => {
  let data: FakeData;
  beforeEach(() => {
    data = fakeData();
    data.permissions = { someone: "write", keeper: "admin", reader: "read", renovate: "write" };
    data.behindBy[SHA] = 0;
    data.prRuns[SHA] = [prRun(PR)];
  });

  // run decides one pull request changing files, counted as listed unless
  // the case says otherwise, and reports whether it was merged.
  async function run(over: Partial<PullRequest>, files: string[], count?: number | null): Promise<boolean> {
    data.files[SHA] = changed(...files);
    data.pulls.set(PR, pull({ changedFiles: count === null ? undefined : (count ?? files.length), ...over }));
    await mergeGreen(fakeGitHub(data), SHA, ONE_PR);
    return data.calls.includes(`merge ${PR} ${SHA}`);
  }

  const cases: [string, boolean, Partial<PullRequest>, string[], (number | null)?][] = [
    ["Go source by a writer", true, {}, ["verifier.go"]],
    ["documentation", true, {}, ["README.md", "docs/guide.md"]],
    ["an admin's", true, { author: { login: "keeper", bot: false } }, ["verifier.go"]],
    ["a draft", false, { draft: true }, ["verifier.go"]],
    ["a fork's", false, { fromFork: true }, ["verifier.go"]],
    ["a closed one", false, { open: false }, ["verifier.go"]],
    ["Renovate's", false, { author: { login: "renovate", bot: true } }, ["verifier.go"]],
    ["a user named renovate is not Renovate", true, { author: { login: "renovate", bot: false } }, ["verifier.go"]],
    ["into another branch", false, { baseRef: "release/v1" }, ["verifier.go"]],
    ["by someone who can only read", false, { author: { login: "reader", bot: false } }, ["verifier.go"]],
    ["by someone the API does not know", false, { author: { login: "stranger", bot: false } }, ["verifier.go"]],
    ["moved past the tested commit", false, { headSha: OTHER_SHA }, ["verifier.go"]],
    ["a workflow", false, {}, [".github/workflows/ci.yaml"]],
    ["a CI script", false, {}, [".github/ci/merge-green.ts"]],
    ["code beside a workflow", false, {}, ["verifier.go", ".github/workflows/ci.yaml"]],
    ["a Taskfile Task prefers", false, {}, ["Taskfile.yml"]],
    ["the linter's configuration", false, {}, [".golangci.yaml"]],
    ["a pinned tool", false, {}, ["tools/golangci-lint/go.mod"]],
    ["the module definition", false, {}, ["go.mod"]],
    ["a workspace", false, {}, ["go.work"]],
    ["a workspace's checksums", false, {}, ["go.work.sum"]],
    ["Task's own settings", false, {}, [".taskrc.yml"]],
    ["Renovate's settings", false, {}, ["renovate.json"]],
    ["Renovate's settings, another spelling", false, {}, [".renovaterc.json5"]],
    // A rename names the path it leaves too.
    ["a workflow renamed out of .github", false, {}, [".github/workflows/codeql.yaml>docs/codeql.yaml"]],
    ["CODEOWNERS renamed away", false, {}, [".github/CODEOWNERS>docs/CODEOWNERS"]],
    ["Renovate's settings renamed away", false, {}, ["renovate.json>docs/renovate.json"]],
    ["a Go file renamed", true, {}, ["old.go>new.go"]],
    ["a workspace in a nested module", false, {}, ["pgstore/go.work"]],
    ["a vendor directory", false, {}, ["vendor/modules.txt"]],
    ["Task's cache of a remote include", false, {}, [".task/remote/taskfile.yaml"]],
    ["a nested vendor directory", false, {}, ["pgstore/vendor/modules.txt"]],
    // A count GitHub did not give is not a complete listing.
    ["a count that is not one", false, {}, ["verifier.go"], null],
    // A file the API did not list could be a CI definition.
    ["more files changed than listed", false, {}, ["verifier.go"], 2],
    // More files listed than GitHub counts: the answers are of different heads.
    ["more files listed than changed", false, {}, ["a.go", "b.go"], 1],
    // The compare API lists no more than its cap.
    ["as many files as the compare API lists", false, {}, Array.from({ length: 300 }, (_, i) => `f${i + 1}.go`)],
  ];
  for (const [name, want, over, files, count] of cases) {
    test(name, async () => assert.equal(await run(over, files, count), want));
  }

  test("the files are listed at the tested commit", async () => {
    await run({}, ["verifier.go"]);
    assert.ok(data.calls.includes(`compare main...${SHA}`), data.calls.join("; "));
  });

  test("a merge GitHub refuses is reported and the run goes on", async () => {
    data.mergeFails.add(PR);
    assert.equal(await run({}, ["verifier.go"]), true);
  });

  test("every pull request of the run is decided", async () => {
    data.files[SHA] = changed("verifier.go");
    data.prRuns[SHA] = [{ ...prRun(7), pullRequests: [{ number: 7, baseRef: "main" }, { number: 8, baseRef: "main" }] }];
    data.pulls.set(7, pull({ number: 7, changedFiles: 1 }));
    data.pulls.set(8, pull({ number: 8, changedFiles: 1 }));
    await mergeGreen(fakeGitHub(data), SHA, JSON.stringify([{ number: 7 }, { number: 8 }]));
    assert.deepEqual(data.calls.filter((c) => c.startsWith("merge")), [`merge 7 ${SHA}`, `merge 8 ${SHA}`]);
  });

  // A nested module's go.mod merges when only its requirements change — the
  // release raises them so — and is held when anything else does.
  const NESTED = "pgstore/go.mod";
  const goMod = (extra = "", limes = "v0.108.3") => `module example.com/pgstore\n\ngo 1.26.0\n\nreplace example.com/limes => ../\n${extra}\nrequire example.com/limes ${limes}\n`;
  const nested: [string, string | undefined, string | undefined, boolean][] = [
    ["a nested module's requirement raised", goMod(), goMod("", "v0.109.0"), true],
    ["a nested module's replace dropped", goMod(), goMod().replace("replace example.com/limes => ../\n", ""), false],
    ["a nested module's toolchain set", goMod(), goMod("toolchain go1.26.0"), false],
    ["a nested module's packages ignored", goMod(), goMod("ignore ./store"), false],
    ["a new nested module", undefined, goMod(), false],
  ];
  for (const [name, before, after, want] of nested) {
    test(name, async () => {
      if (before !== undefined) data.contents[`main:${NESTED}`] = before;
      if (after !== undefined) data.contents[`${SHA}:${NESTED}`] = after;
      assert.equal(await run({}, [NESTED, "pgstore/go.sum"]), want);
    });
  }

  // The verdict that merges a pull request is its own run's into main: a
  // green run of another pull request at the same commit is not.
  const verdicts: [string, boolean, ReturnType<typeof prRun>[]][] = [
    ["its own run passed", true, [prRun(PR)]],
    ["its own run failed", false, [prRun(PR, "failure")]],
    ["its newest run is still going", false, [prRun(PR, RUNNING), prRun(PR)]],
    ["only another pull request's run passed", false, [prRun(PR + 1)]],
    ["only a run into another base passed", false, [prRun(PR, "success", "release/v1")]],
    ["its own run failed beside another's that passed", false, [prRun(PR + 1), prRun(PR, "failure")]],
    ["no run at all", false, []],
  ];
  for (const [name, want, runs] of verdicts) {
    test(name, async () => {
      data.prRuns[SHA] = runs;
      assert.equal(await run({}, ["verifier.go"]), want);
    });
  }

  // The merge commit's body is the title the run checked, not the title now.
  test("the merge commit carries the title its run checked", async () => {
    data.prRuns[SHA] = [prRun(PR, "success", "main", "fix: what was checked")];
    await run({}, ["verifier.go"]);
    assert.equal(data.merged.get(PR), "fix: what was checked");
    assert.notEqual(data.merged.get(PR), PULL_REQUEST_TITLE);
  });

  // Inputs that are not what the workflow passes are refused, not used.
  test("a HEAD_SHA that is not a commit id", async () => {
    await assert.rejects(mergeGreen(fakeGitHub(data), "main", ONE_PR), /not a commit id/);
    assert.deepEqual(data.calls, []);
  });
});

describe("pullRequestNumbers", () => {
  test("reads the numbers", () => assert.deepEqual(pullRequestNumbers('[{"number": 7}, {"number": 8, "head": {}}]'), [7, 8]));
  for (const bad of ['[{"number": "7; rm -rf /"}]', '[{"number": 1.5}]', '[{"number": -1}]', '[{}]', '{"number": 7}']) {
    test(`refuses ${bad}`, () => assert.throws(() => pullRequestNumbers(bad)));
  }
});

// Why a pull request's own run does not let it merge, each case named.
describe("unpassed", () => {
  test("passed", () => assert.equal(unpassed(pull(), prRun(PR)), undefined));
  test("still going", () => assert.match(unpassed(pull(), prRun(PR, RUNNING)) ?? "", /still going/));
  test("failed", () => assert.match(unpassed(pull(), prRun(PR, "failure")) ?? "", /ended as failure/));
  test("no run", () => assert.match(unpassed(pull(), undefined) ?? "", /no CI run of its own/));
});
