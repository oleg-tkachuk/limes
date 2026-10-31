import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { beforeEach, describe, test } from "node:test";

import { parse } from "yaml";

import { fakeData, fakeGitHub, type FakeData } from "./lib/fake-github.ts";
import type { GitHub, Job, WorkflowRun } from "./lib/github.ts";
import { ciRunArgs, Command, REQUIRED_CHECK_JOB, passedRunFor, problemsWithRun, requiredJobs, runIdOf, vouch } from "./ci-run.ts";
import { CI_WORKFLOW } from "./lib/runs.ts";

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

describe("vouch", () => {
  let data: FakeData;
  let current: GitHub;
  let waits: number[];
  const sleep = async (ms: number) => void waits.push(ms);
  beforeEach(() => {
    data = fakeData();
    waits = [];
    data.runs.set(RUN, good);
  });
  // answers sets the jobs the API lists on each successive read, the last
  // one standing after.
  function answers(...reads: Job[][]): void {
    const github = fakeGitHub(data);
    let read = 0;
    github.runJobs = async () => reads[Math.min(read++, reads.length - 1)] ?? [];
    current = github;
  }
  const run = (attempts = 4) => vouch(current, REPO, RUN, SHA, REQUIRED, { attempts, delayMs: 10, sleep });

  test("a passed run vouches at once", async () => {
    answers(jobsAll("success"));
    await run();
    assert.deepEqual(waits, []);
  });
  test("a job read as unfinished is asked about again", async () => {
    answers(jobsAll("", "in_progress"), jobsAll("", "in_progress"), jobsAll("success"));
    await run();
    assert.deepEqual(waits, [10, 20]);
  });
  test("a job not listed yet is asked about again", async () => {
    answers(jobsAll("success").filter((j) => j.name !== "Verify"), jobsAll("success"));
    await run();
    assert.deepEqual(waits, [10]);
  });
  test("a job unfinished on every read fails once the attempts run out", async () => {
    answers(jobsAll("", "in_progress"));
    await assert.rejects(run(3), /does not vouch for .*job 'Verify' is in_progress/);
    assert.deepEqual(waits, [10, 20]);
  });
  test("a failed job fails at once", async () => {
    answers(jobsAll("failure"));
    await assert.rejects(run(), /job 'Verify' ended as failure/);
    assert.deepEqual(waits, []);
  });
  test("a run of another commit fails at once, unfinished jobs and all", async () => {
    data.runs.set(RUN, { ...good, headSha: OTHER });
    answers(jobsAll("", "in_progress"));
    await assert.rejects(run(), /run tested/);
    assert.deepEqual(waits, []);
  });
});

describe("runIdOf", () => {
  test("a number", () => assert.equal(runIdOf("42"), 42));
  for (const bad of ["", "4 2", "-1", "x"]) {
    test(`refuses ${JSON.stringify(bad)}`, () => assert.throws(() => runIdOf(bad)));
  }
});

// CiJob is what these tests read of a ci.yaml job.
interface CiJob {
  if?: string;
  needs?: string[];
}

const ciJobs = () => (parse(CI) as { jobs: Record<string, CiJob> }).jobs;

// dispatch-release waits for the required check, and a documentation-only
// push dispatches no release.
test("ci.yaml's dispatch waits for the required check", () => {
  const jobs = ciJobs();
  assert.ok((jobs["dispatch-release"]?.needs ?? []).includes("checks"), "dispatch-release does not wait for the required check");
  assert.ok(REQUIRED.includes("Verify") && REQUIRED.length > 1);
  assert.match(jobs["dispatch-release"]?.if ?? "", /needs\.changes\.outputs\.code == 'true'/);
});

describe("passedRunFor", () => {
  const find = (data: FakeData, sha = SHA) => passedRunFor(fakeGitHub(data), REPO, sha, REQUIRED);
  test("the newest passed run for the commit", async () => {
    const data = fakeData();
    for (const id of [7, 9]) {
      data.runs.set(id, good);
      data.jobs.set(id, jobsAll("success"));
    }
    data.runs.set(11, { ...good, headSha: OTHER });
    data.jobs.set(11, jobsAll("success"));
    assert.equal(await find(data), 9);
  });
  // A job outside the required check fails the run without stopping a
  // release.
  test("a run whose only failure is outside the required check", async () => {
    const data = fakeData();
    data.runs.set(9, good);
    data.jobs.set(9, [...jobsAll("success"), { name: "Benchmarks", status: "completed", conclusion: "failure" }]);
    assert.equal(await find(data), 9);
  });
  test("a newer run that failed a required job is passed over", async () => {
    const data = fakeData();
    data.runs.set(7, good);
    data.jobs.set(7, jobsAll("success"));
    data.runs.set(9, good);
    data.jobs.set(9, jobsAll("failure"));
    assert.equal(await find(data), 7);
  });
  test("a run that failed a required job is none", async () => {
    const data = fakeData();
    data.runs.set(9, good);
    data.jobs.set(9, jobsAll("failure"));
    await assert.rejects(find(data), /no passed CI run/);
  });
  test("none is an error", async () => assert.rejects(find(fakeData()), /no passed CI run/));
  test("a sha that is not one", async () => assert.rejects(find(fakeData(), "main")));
});

describe("ciRunArgs", () => {
  test("a run vouching for a sha", () => assert.deepEqual(ciRunArgs([Command.Vouches, "7", "abc"]), { command: Command.Vouches, runText: "7", arg: "abc" }));
  test("a find names a sha alone", () => assert.deepEqual(ciRunArgs([Command.Find, "abc"]), { command: Command.Find, runText: "abc", arg: "" }));
  for (const args of [[], ["rerun", "7"]]) {
    test(`refuses ${JSON.stringify(args)}`, () => assert.throws(() => ciRunArgs(args), /usage: ci-run\.ts/));
  }
});

describe("requiredJobs", () => {
  test("a job the check needs alone, by its name", () => {
    assert.deepEqual(requiredJobs(`jobs:\n  ${REQUIRED_CHECK_JOB}:\n    needs: verify\n  verify:\n    name: Verify\n`), ["Verify"]);
  });
  test("a job with no name of its own, by its id", () => {
    assert.deepEqual(requiredJobs(`jobs:\n  ${REQUIRED_CHECK_JOB}:\n    needs: [lint]\n  lint: {}\n`), ["lint"]);
  });
  test("a check that needs nothing requires nothing", () => {
    assert.deepEqual(requiredJobs(`jobs:\n  ${REQUIRED_CHECK_JOB}: {}\n`), []);
  });
  test("a workflow without the check is refused", () => {
    assert.throws(() => requiredJobs("jobs:\n  lint: {}\n"), /has no/);
  });
});
