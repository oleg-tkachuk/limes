// What the CI run that dispatched a release says: whether it vouches for the
// commit.
//
// Usage: GH_TOKEN=… GITHUB_REPOSITORY=… node ci-run.ts vouches <run-id> <sha>
//        GH_TOKEN=… GITHUB_REPOSITORY=… node ci-run.ts find <sha>
//
// vouches fails unless the run is ci.yaml's, on a push to main, in this
// repository, for sha, and every job the required check stands for passed or
// was skipped: the payload of a repository_dispatch is its sender's to
// write, so only the API's answer about the run it names is trusted. A job
// that reads as unfinished is asked about again before that fails.
// find sets the step output `run-id` to the newest finished push run of
// ci.yaml on main for sha that vouches for it, as vouches asks, for a release
// asked for by hand, which no CI run dispatched; none is an error. A run is
// not asked to have passed whole: a job outside the required check fails the
// run without stopping a release.

import { readFileSync } from "node:fs";

import { parse } from "yaml";

import { info, setOutput } from "./lib/actions.ts";
import { requireEnv } from "./lib/env.ts";
import { gitHubFor, type GitHub, type Job, type WorkflowRun } from "./lib/github.ts";
import { runMain } from "./lib/main.ts";
import { retry, type RetryOptions } from "./lib/retry.ts";
import { CI_WORKFLOW, COMPLETED, PASSING } from "./lib/runs.ts";

// REQUIRED_CHECK_JOB is the id of the job main requires, whose needs are the
// jobs a merge stands on.
export const REQUIRED_CHECK_JOB = "checks";
const RELEASE_BRANCH = "main";
const PUSH_EVENT = "push";
// OUTPUT_RUN_ID is the step output find sets.
export const OUTPUT_RUN_ID = "run-id";

// Command names what a step asks.
export const Command = { Vouches: "vouches", Find: "find" } as const;

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

// Verdict is every reason a run does not vouch for a commit — none when it
// does — and whether each is a job not finished, or not listed, yet: what
// the API may answer differently a moment later.
export interface Verdict {
  problems: string[];
  pending: boolean;
}

// problemsWithRun is every reason run does not vouch for sha; none when it
// does.
export async function problemsWithRun(github: GitHub, repository: string, runId: number, sha: string, required: readonly string[]): Promise<string[]> {
  return (await verdictOn(github, repository, runId, sha, required)).problems;
}

// verdictOn is problemsWithRun's answer, with whether it is only pending.
export async function verdictOn(github: GitHub, repository: string, runId: number, sha: string, required: readonly string[]): Promise<Verdict> {
  if (!COMMIT_ID.test(sha)) {
    return { problems: [`sha ${JSON.stringify(sha)} is not a commit id`], pending: false };
  }
  const settled = runProblems(await github.workflowRun(runId), repository, sha);
  const jobs = jobProblems(await github.runJobs(runId), required);
  const problems = [...settled, ...jobs.problems];
  return { problems, pending: settled.length === 0 && jobs.unfinished > 0 && jobs.unfinished === problems.length };
}

// runProblems are why run is not the CI run of a push of sha to the release
// branch of repository.
function runProblems(run: WorkflowRun, repository: string, sha: string): string[] {
  const checks: [boolean, string][] = [
    [run.path !== CI_WORKFLOW, `run is ${run.path}, not ${CI_WORKFLOW}`],
    [run.event !== PUSH_EVENT, `run was triggered by ${run.event}, not ${PUSH_EVENT}`],
    [run.headBranch !== RELEASE_BRANCH, `run is on ${run.headBranch}, not ${RELEASE_BRANCH}`],
    [run.headSha !== sha, `run tested ${run.headSha}, not ${sha}`],
    [run.repository !== repository, `run belongs to ${run.repository}`],
  ];
  return checks.filter(([wrong]) => wrong).map(([, problem]) => problem);
}

// jobProblems are why each of required, in jobs, has not passed, and how
// many of them have not finished.
function jobProblems(jobs: Job[], required: readonly string[]): { problems: string[]; unfinished: number } {
  const problems: string[] = [];
  let unfinished = 0;
  for (const name of required) {
    const job = latestJob(jobs, name);
    if (job === undefined || job.status !== COMPLETED) {
      problems.push(`job '${name}' is ${job?.status ?? "missing"}`);
      unfinished++;
    } else if (!PASSING.includes(job.conclusion ?? "")) {
      problems.push(`job '${name}' ended as ${job.conclusion ?? "nothing"}`);
    }
  }
  return { problems, unfinished };
}

// RunPending is a run that does not vouch yet only because a job it needs
// reads as unfinished.
export class RunPending extends Error {}

// vouch returns once run vouches for sha, and fails as soon as it cannot: a
// run of another commit, branch, event or workflow, or a job that ended
// otherwise than passed or skipped. A run that only reads a job as
// unfinished is asked again, a while later each time (retry): the API has
// answered in_progress for a job minutes after it ended.
export async function vouch(github: GitHub, repository: string, runId: number, sha: string, required: readonly string[], options: RetryOptions = {}): Promise<void> {
  await retry(
    `CI run ${runId}`,
    async () => {
      const verdict = await verdictOn(github, repository, runId, sha, required);
      if (verdict.problems.length === 0) {
        return;
      }
      const message = `CI run ${runId} does not vouch for ${sha}: ${verdict.problems.join("; ")}`;
      throw verdict.pending ? new RunPending(message) : new Error(message);
    },
    { ...options, retryable: (err) => err instanceof RunPending },
  );
}

// passedRunFor is the newest finished push run of ci.yaml on main for sha
// that vouches for it: every job of required passed or was skipped.
export async function passedRunFor(github: GitHub, repository: string, sha: string, required: readonly string[]): Promise<number> {
  if (!COMMIT_ID.test(sha)) {
    throw new Error(`sha ${JSON.stringify(sha)} is not a commit id`);
  }
  for (const runId of await github.finishedRunsOf(CI_WORKFLOW, RELEASE_BRANCH, sha)) {
    const problems = await problemsWithRun(github, repository, runId, sha, required);
    if (problems.length === 0) {
      return runId;
    }
    info(`CI run ${runId} does not vouch for ${sha}: ${problems.join("; ")}`);
  }
  throw new Error(`no passed CI run on ${RELEASE_BRANCH} for ${sha}: a release is cut only from a commit CI passed`);
}

async function main(): Promise<void> {
  const { command, runText, arg } = ciRunArgs(process.argv.slice(2));
  const repository = requireEnv("GITHUB_REPOSITORY");
  const github = gitHubFor(requireEnv("GH_TOKEN"), repository);
  const required = () => requiredJobs(readFileSync(CI_WORKFLOW, "utf8"));
  const commands: Record<CiRunCommand, () => Promise<void>> = {
    [Command.Find]: async () => setOutput(OUTPUT_RUN_ID, String(await passedRunFor(github, repository, runText, required()))),
    [Command.Vouches]: async () => {
      const runId = runIdOf(runText);
      await vouch(github, repository, runId, arg, required());
      info(`CI run ${runId} passed for ${arg}`);
    },
  };
  await commands[command]();
}

// CiRunCommand is a command ci-run.ts runs.
export type CiRunCommand = (typeof Command)[keyof typeof Command];

// ciRunArgs reads the command, the run (or, for find, the sha) and the
// command's argument from args, refusing a command this script does not run.
export function ciRunArgs(args: readonly string[]): { command: CiRunCommand; runText: string; arg: string } {
  const [command, runText = "", arg = ""] = args;
  if (!Object.values(Command).includes(command as CiRunCommand)) {
    throw new Error(`usage: ci-run.ts ${Command.Vouches} <run-id> <sha> | ${Command.Find} <sha>`);
  }
  return { command: command as CiRunCommand, runText, arg };
}

if (import.meta.main) {
  await runMain(main);
}
