import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { beforeEach, describe, test } from "node:test";

import { parse } from "yaml";

import { fakeData, fakeGitHub, type FakeData } from "./lib/fake-github.ts";
import type { GitHub, Job, WorkflowRun } from "./lib/github.ts";
import { jobPassed, passedRunFor, problemsWithRun, requiredJobs, runIdOf, vouch } from "./ci-run.ts";
import { CI_WORKFLOW } from "./lib/runs.ts";
import { GATED, GATES, gateOf } from "./release.ts";

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

describe("jobPassed", () => {
  const JOB = gateOf("pgstore");
  async function passed(jobs: Job[], name = JOB): Promise<boolean> {
    const data = fakeData();
    data.jobs.set(RUN, jobs);
    return jobPassed(fakeGitHub(data), RUN, name);
  }
  test("a passed job", async () => assert.equal(await passed([{ name: JOB, status: "completed", conclusion: "success" }]), true));
  test("a skipped job is no pass", async () => assert.equal(await passed([{ name: JOB, status: "completed", conclusion: "skipped" }]), false));
  test("a failed job", async () => assert.equal(await passed([{ name: JOB, status: "completed", conclusion: "failure" }]), false));
  test("a cancelled job", async () => assert.equal(await passed([{ name: JOB, status: "completed", conclusion: "cancelled" }]), false));
  test("a running job", async () => assert.rejects(passed([{ name: JOB, status: "in_progress", conclusion: undefined }])));
  test("a missing job", async () => assert.rejects(passed([{ name: "Verify", status: "completed", conclusion: "success" }])));
  test("no job name", async () => assert.rejects(passed([], "")));
});

// CiJob is what these tests read of a ci.yaml job.
interface CiJob {
  name?: string;
  if?: string;
  needs?: string[];
  strategy?: { matrix?: { module?: string[] } };
}
const MATRIX_MODULE = "${{ matrix.module }}";

// jobNamed is the id and job of ci.yaml that runs as name, a matrix job
// once for each of its modules.
function jobNamed(jobs: Record<string, CiJob>, name: string): [string, CiJob] | undefined {
  return Object.entries(jobs).find(([, j]) =>
    (j.strategy?.matrix?.module ?? [undefined]).some((m) => (m === undefined ? j.name : j.name?.replaceAll(MATRIX_MODULE, m)) === name),
  );
}

const ciJobs = () => (parse(CI) as { jobs: Record<string, CiJob> }).jobs;

// The jobs the release reads are ones ci.yaml runs, and dispatch-release
// waits for them and for the required check.
test("ci.yaml's dispatch waits for what the release reads", () => {
  const jobs = ciJobs();
  const waited = jobs["dispatch-release"]?.needs ?? [];
  assert.ok(waited.includes("checks"), "dispatch-release does not wait for the required check");
  for (const gate of Object.values(GATES)) {
    const id = jobNamed(jobs, gate)?.[0];
    assert.ok(id !== undefined && waited.includes(id), `dispatch-release does not wait for '${gate}'`);
  }
  assert.ok(REQUIRED.includes("Verify") && REQUIRED.length > 1);
});

// A gate the release reads runs on every push to main that reaches code: a
// push that skipped it would read as a pass, and let through a module an
// earlier run held back.
test("every release gate runs on a push to main", () => {
  const jobs = ciJobs();
  for (const gate of Object.values(GATES)) {
    const job = jobNamed(jobs, gate)?.[1];
    assert.match(job?.if ?? "", /github\.event_name == 'push' && needs\.changes\.outputs\.relevant == 'true'/, gate);
    // And on no pull request, where nothing reads it and a change that lands
    // with its module fails it until limes is released.
    assert.doesNotMatch(job?.if ?? "", /\|\||pull_request|outputs\.(pgstore|redisreplay|otelmetric)/, gate);
  }
  // And a documentation-only push dispatches no release.
  const dispatch = jobs["dispatch-release"]?.if ?? "";
  assert.match(dispatch, /needs\.changes\.outputs\.code == 'true'/);
});

// The released-limes job runs once for each gated module, and for no other:
// a module it skipped would never be released, one the release does not read
// would gate nothing.
test("the released-limes matrix is the gated modules", () => {
  const job = jobNamed(ciJobs(), gateOf(GATED[0] ?? ""))?.[1];
  assert.deepEqual([...(job?.strategy?.matrix?.module ?? [])].sort(), [...GATED].sort());
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
  // A released-limes gate fails the run while a nested module follows a limes
  // not yet released; the release reads that gate on its own.
  test("a run whose only failure is outside the required check", async () => {
    const data = fakeData();
    data.runs.set(9, good);
    data.jobs.set(9, [...jobsAll("success"), { name: "Released limes, pgstore", status: "completed", conclusion: "failure" }]);
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
