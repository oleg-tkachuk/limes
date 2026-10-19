import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { beforeEach, describe, test } from "node:test";

import { parse } from "yaml";

import { fakeData, fakeGitHub, type FakeData } from "./lib/fake-github.ts";
import type { Job, WorkflowRun } from "./lib/github.ts";
import { CI_WORKFLOW, jobPassed, problemsWithRun, requiredJobs, runIdOf } from "./ci-run.ts";
import { GATES } from "./release.ts";

const REPO = "oleg-tkachuk/limes";
const SHA = "0123456789abcdef0123456789abcdef01234567";
const OTHER = "fedcba9876543210fedcba9876543210fedcba98";
const RUN = 42;
const CI = readFileSync(new URL(`../../${CI_WORKFLOW}`, import.meta.url), "utf8");
const REQUIRED = requiredJobs(CI);

const good: WorkflowRun = { path: CI_WORKFLOW, event: "push", headBranch: "main", headSha: SHA, repository: REPO };
const jobsAll = (conclusion: string, status = "completed"): Job[] =>
  REQUIRED.map((name) => ({ name, status: name === "Verify" ? status : "completed", conclusion: name === "Verify" ? conclusion : "success" }));

describe("problemsWithRun", () => {
  let data: FakeData;
  beforeEach(() => {
    data = fakeData();
  });
  async function vouches(run: WorkflowRun, jobs: Job[], sha = SHA): Promise<boolean> {
    data.runs.set(RUN, run);
    data.jobs.set(RUN, jobs);
    return (await problemsWithRun(fakeGitHub(data), REPO, RUN, sha, REQUIRED)).length === 0;
  }
  const cases: [string, boolean, () => [WorkflowRun, Job[], string?]][] = [
    ["a passed run", true, () => [good, jobsAll("success")]],
    ["a skipped job", true, () => [good, jobsAll("skipped")]],
    ["a failed job", false, () => [good, jobsAll("failure")]],
    ["a running job", false, () => [good, jobsAll("", "in_progress")]],
    ["a missing job", false, () => [good, jobsAll("success").filter((j) => j.name !== "Verify")]],
    ["another sha", false, () => [{ ...good, headSha: OTHER }, jobsAll("success")]],
    ["another workflow", false, () => [{ ...good, path: ".github/workflows/codeql.yaml" }, jobsAll("success")]],
    ["a pull request", false, () => [{ ...good, event: "pull_request" }, jobsAll("success")]],
    ["another branch", false, () => [{ ...good, headBranch: "feature" }, jobsAll("success")]],
    ["another repository", false, () => [{ ...good, repository: "someone/fork" }, jobsAll("success")]],
    ["no sha", false, () => [good, jobsAll("success"), ""]],
  ];
  for (const [name, want, args] of cases) {
    test(name, async () => {
      const [run, jobs, sha] = args();
      assert.equal(await vouches(run, jobs, sha), want);
    });
  }
});

describe("runIdOf", () => {
  test("a number", () => assert.equal(runIdOf("42"), 42));
  for (const bad of ["", "4 2", "-1", "x"]) {
    test(`refuses ${JSON.stringify(bad)}`, () => assert.throws(() => runIdOf(bad)));
  }
});

describe("jobPassed", () => {
  const JOB = "PostgreSQL store, released limes";
  async function passed(jobs: Job[], name = JOB): Promise<boolean> {
    const data = fakeData();
    data.jobs.set(RUN, jobs);
    return jobPassed(fakeGitHub(data), RUN, name);
  }
  test("a passed job", async () => assert.equal(await passed([{ name: JOB, status: "completed", conclusion: "success" }]), true));
  test("a skipped job", async () => assert.equal(await passed([{ name: JOB, status: "completed", conclusion: "skipped" }]), true));
  test("a failed job", async () => assert.equal(await passed([{ name: JOB, status: "completed", conclusion: "failure" }]), false));
  test("a cancelled job", async () => assert.equal(await passed([{ name: JOB, status: "completed", conclusion: "cancelled" }]), false));
  test("a running job", async () => assert.rejects(passed([{ name: JOB, status: "in_progress", conclusion: undefined }])));
  test("a missing job", async () => assert.rejects(passed([{ name: "Verify", status: "completed", conclusion: "success" }])));
  test("no job name", async () => assert.rejects(passed([], "")));
});

// The jobs the release reads are ones ci.yaml runs, and dispatch-release
// waits for them and for the required check.
test("ci.yaml's dispatch waits for what the release reads", () => {
  const doc = parse(CI) as { jobs: Record<string, { name?: string; needs?: string[] }> };
  const waited = doc.jobs["dispatch-release"]?.needs ?? [];
  assert.ok(waited.includes("checks"), "dispatch-release does not wait for the required check");
  for (const gate of Object.values(GATES)) {
    const id = Object.entries(doc.jobs).find(([, j]) => j.name === gate)?.[0];
    assert.ok(id !== undefined && waited.includes(id), `dispatch-release does not wait for '${gate}'`);
  }
  assert.ok(REQUIRED.includes("Verify") && REQUIRED.length > 1);
});
