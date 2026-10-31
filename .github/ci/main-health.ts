// Say when main is red, and merge what waited once it is green again.
//
// Usage: ISSUES_TOKEN=… GITHUB_REPOSITORY=… RUN_ID=… CONCLUSION=… HEAD_SHA=…
//        RUN_URL=… [GH_TOKEN=…] node main-health.ts
//
// auto-merge.yaml runs this from main when ci.yaml completes on a push to
// main. A run is read by its required check (requiredConclusion): a job
// outside it says nothing about main. A run that
// failed opens an issue titled RED_ISSUE, or comments on the
// one still open, so a broken main is seen, not only read in a log; while it
// is red, merge-green merges nothing. A run that passed closes that issue and
// merges, through GH_TOKEN (AUTOMERGE_TOKEN), each open pull request whose
// own CI run into main passed at its head: the ones the red main held back
// (merge-green.ts). A run
// that ended otherwise — cancelled, skipped — says nothing about main.

import { info, summarize, warning } from "./lib/actions.ts";
import { optionalEnv, requireEnv } from "./lib/env.ts";
import { gitHubFor, type GitHub } from "./lib/github.ts";
import { runMain } from "./lib/main.ts";
import { RED, SUCCESS, requiredConclusion } from "./lib/runs.ts";
import { mergeWaiting } from "./merge-green.ts";

// RED_ISSUE is the title of the issue that stands open while main is red.
export const RED_ISSUE = "CI is failing on main";

// Run is the CI run on main this reports on.
export interface Run {
  conclusion: string;
  headSha: string;
  url: string;
}

// State is what main's run says about main.
export const State = { Red: "red", Green: "green", Unknown: "unknown" } as const;
export type State = (typeof State)[keyof typeof State];

// stateOf is what a run's conclusion says about main.
export function stateOf(conclusion: string): State {
  if (conclusion === SUCCESS) return State.Green;
  return RED.includes(conclusion) ? State.Red : State.Unknown;
}

// reportHealth opens, comments on or closes the red-main issue for run, and
// says what main is.
export async function reportHealth(issues: GitHub, run: Run): Promise<State> {
  const state = stateOf(run.conclusion);
  const open = await issues.openIssueTitled(RED_ISSUE);
  const at = `${run.headSha.slice(0, 7)} (${run.url})`;
  if (state === State.Red) {
    if (open === undefined) {
      await issues.createIssue(
        RED_ISSUE,
        `CI failed on main at ${at}. Nothing is merged automatically until main is green again; this closes itself then.`,
      );
    } else {
      await issues.commentOnIssue(open, `Still failing at ${at}.`);
    }
    warning(`main is red at ${at}`);
  } else if (state === State.Green && open !== undefined) {
    await issues.commentOnIssue(open, `Green again at ${at}.`);
    await issues.closeIssue(open);
    info(`main is green again at ${at}`);
  }
  summarize(`### main is ${state}\n\nCI on main at ${at}.\n`);
  return state;
}

async function main(): Promise<void> {
  const repository = requireEnv("GITHUB_REPOSITORY");
  const issues = gitHubFor(requireEnv("ISSUES_TOKEN"), repository);
  const runId = runIdFrom(requireEnv("RUN_ID"));
  const conclusion = await requiredConclusion(issues, { id: runId, conclusion: requireEnv("CONCLUSION") });
  const run: Run = { conclusion, headSha: requireEnv("HEAD_SHA"), url: requireEnv("RUN_URL") };
  const state = await reportHealth(issues, run);
  await mergeWhenGreen(state, optionalEnv("GH_TOKEN"), (token) => mergeWaiting(gitHubFor(token, repository)));
}

// runIdFrom reads a run id, refusing what is no positive integer.
export function runIdFrom(text: string): number {
  const runId = Number(text);
  if (!Number.isSafeInteger(runId) || runId <= 0) {
    throw new Error(`RUN_ID ${text} is not a run id`);
  }
  return runId;
}

// mergeWhenGreen merges the pull requests waiting on main, with token,
// once main is green; without a token, it says why nothing is merged.
export async function mergeWhenGreen(state: State, token: string | undefined, merge: (token: string) => Promise<unknown>): Promise<void> {
  if (state !== State.Green) {
    return;
  }
  if (token === undefined) {
    warning("AUTOMERGE_TOKEN is not set; nothing merged (see auto-merge.yaml)");
    return;
  }
  await merge(token);
}

if (import.meta.main) {
  await runMain(main);
}
