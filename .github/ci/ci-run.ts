// What the CI run that dispatched a release says: whether it vouches for the
// commit, and whether one of its jobs passed.
//
// Usage: GH_TOKEN=… GITHUB_REPOSITORY=… node ci-run.ts vouches <run-id> <sha>
//        GH_TOKEN=… GITHUB_REPOSITORY=… node ci-run.ts job-passed <run-id> <job-name>
//        GH_TOKEN=… GITHUB_REPOSITORY=… node ci-run.ts find <sha>
//
// vouches fails unless the run is ci.yaml's, on a push to main, in this
// repository, for sha, and every job the required check stands for passed or
// was skipped: the payload of a repository_dispatch is its sender's to
// write, so only the API's answer about the run it names is trusted.
// job-passed sets the step output `passed`: true for a job that passed or was
// skipped, false for one that failed; one missing or unfinished is an error.
// find sets the step output `run-id` to the newest passed push run of
// ci.yaml on main for sha, for a release asked for by hand, which no CI run
// dispatched; none is an error.

import { readFileSync } from "node:fs";

import { parse } from "yaml";

import { info, setOutput } from "./lib/actions.ts";
import { requireEnv } from "./lib/env.ts";
import { gitHubFor, type GitHub, type Job } from "./lib/github.ts";
import { runMain } from "./lib/main.ts";
import { CI_WORKFLOW, COMPLETED, PASSING } from "./lib/runs.ts";

// REQUIRED_CHECK_JOB is the id of the job main requires, whose needs are the
// jobs a merge stands on.
const REQUIRED_CHECK_JOB = "checks";
const RELEASE_BRANCH = "main";
const PUSH_EVENT = "push";
// OUTPUT_PASSED is the step output job-passed sets, OUTPUT_RUN_ID the one
// find sets.
export const OUTPUT_PASSED = "passed";
export const OUTPUT_RUN_ID = "run-id";

// Command names what a step asks.
export const Command = { Vouches: "vouches", JobPassed: "job-passed", Find: "find" } as const;

// requiredJobs is the name of every job the required check needs, as the
// workflow text ci.yaml declares them: a job's name, or its id without one.
export function requiredJobs(workflow: string): string[] {
  const doc = parse(workflow) as { jobs: Record<string, { name?: string; needs?: string[] | string }> };
  const check = doc.jobs[REQUIRED_CHECK_JOB];
  if (check === undefined) {
    throw new Error(`${CI_WORKFLOW} has no ${REQUIRED_CHECK_JOB} job`);
  }
  const needs = typeof check.needs === "string" ? [check.needs] : (check.needs ?? []);
  return needs.map((id) => doc.jobs[id]?.name ?? id);
}

// latestJob is the last run of the job named name, if any.
function latestJob(jobs: readonly Job[], name: string): Job | undefined {
  return jobs.filter((j) => j.name === name).at(-1);
}

const RUN_ID = /^[0-9]+$/;
const COMMIT_ID = /^[0-9a-f]{40}$/;

// runIdOf reads a run id, refusing anything else.
export function runIdOf(text: string): number {
  if (!RUN_ID.test(text)) {
    throw new Error(`run id ${JSON.stringify(text)} is not a number`);
  }
  return Number(text);
}

// problemsWithRun is every reason run does not vouch for sha; none when it
// does.
export async function problemsWithRun(github: GitHub, repository: string, runId: number, sha: string, required: readonly string[]): Promise<string[]> {
  if (!COMMIT_ID.test(sha)) {
    return [`sha ${JSON.stringify(sha)} is not a commit id`];
  }
  const run = await github.workflowRun(runId);
  const problems: string[] = [];
  if (run.path !== CI_WORKFLOW) problems.push(`run is ${run.path}, not ${CI_WORKFLOW}`);
  if (run.event !== PUSH_EVENT) problems.push(`run was triggered by ${run.event}, not ${PUSH_EVENT}`);
  if (run.headBranch !== RELEASE_BRANCH) problems.push(`run is on ${run.headBranch}, not ${RELEASE_BRANCH}`);
  if (run.headSha !== sha) problems.push(`run tested ${run.headSha}, not ${sha}`);
  if (run.repository !== repository) problems.push(`run belongs to ${run.repository}`);
  const jobs = await github.runJobs(runId);
  for (const name of required) {
    const job = latestJob(jobs, name);
    if (job === undefined || job.status !== COMPLETED) {
      problems.push(`job '${name}' is ${job?.status ?? "missing"}`);
    } else if (!PASSING.includes(job.conclusion ?? "")) {
      problems.push(`job '${name}' ended as ${job.conclusion ?? "nothing"}`);
    }
  }
  return problems;
}

// jobPassed reports whether the job named name passed or was skipped; one
// missing or unfinished is an error.
export async function jobPassed(github: GitHub, runId: number, name: string): Promise<boolean> {
  if (name === "") {
    throw new Error("no job name");
  }
  const job = latestJob(await github.runJobs(runId), name);
  if (job === undefined || job.status !== COMPLETED) {
    throw new Error(`job '${name}' of CI run ${runId} is ${job?.status ?? "missing"}`);
  }
  if (!PASSING.includes(job.conclusion ?? "")) {
    info(`job '${name}' of CI run ${runId} ended as ${job.conclusion ?? "nothing"}`);
    return false;
  }
  return true;
}

// passedRunFor is the newest passed push run of ci.yaml on main for sha.
export async function passedRunFor(github: GitHub, sha: string): Promise<number> {
  if (!COMMIT_ID.test(sha)) {
    throw new Error(`sha ${JSON.stringify(sha)} is not a commit id`);
  }
  const [runId] = await github.passedRunsOf(CI_WORKFLOW, RELEASE_BRANCH, sha);
  if (runId === undefined) {
    throw new Error(`no passed CI run on ${RELEASE_BRANCH} for ${sha}: a release is cut only from a commit CI passed`);
  }
  return runId;
}

async function main(): Promise<void> {
  const [command, runText = "", arg = ""] = process.argv.slice(2);
  const repository = requireEnv("GITHUB_REPOSITORY");
  const github = gitHubFor(requireEnv("GH_TOKEN"), repository);
  if (command === Command.Find) {
    setOutput(OUTPUT_RUN_ID, String(await passedRunFor(github, runText)));
    return;
  }
  const runId = runIdOf(runText);
  switch (command) {
    case Command.Vouches: {
      const problems = await problemsWithRun(github, repository, runId, arg, requiredJobs(readFileSync(CI_WORKFLOW, "utf8")));
      if (problems.length > 0) {
        throw new Error(`CI run ${runId} does not vouch for ${arg}: ${problems.join("; ")}`);
      }
      info(`CI run ${runId} passed for ${arg}`);
      break;
    }
    case Command.JobPassed:
      setOutput(OUTPUT_PASSED, String(await jobPassed(github, runId, arg)));
      break;
    default:
      throw new Error(`usage: ci-run.ts ${Command.Vouches}|${Command.JobPassed} <run-id> <sha|job> | ${Command.Find} <sha>`);
  }
}

if (import.meta.main) {
  await runMain(main);
}
