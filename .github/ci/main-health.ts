// Say when main is red, and merge what waited once it is green again.
//
// Usage: ISSUES_TOKEN=… GITHUB_REPOSITORY=… CONCLUSION=… HEAD_SHA=… RUN_URL=…
//        [GH_TOKEN=…] node main-health.ts
//
// auto-merge.yaml runs this from main when ci.yaml completes on a push to
// main. A run that failed opens an issue titled RED_ISSUE, or comments on the
// one still open, so a broken main is seen, not only read in a log; while it
// is red, merge-green merges nothing. A run that passed closes that issue and
// merges, through GH_TOKEN (AUTOMERGE_TOKEN), each open pull request whose
// required check passed at its head: the ones the red main held back. A run
// that ended otherwise — cancelled, skipped — says nothing about main.

import { info, summarize, warning } from "./lib/actions.ts";
import { optionalEnv, requireEnv } from "./lib/env.ts";
import { gitHubFor, type GitHub } from "./lib/github.ts";
import { runMain } from "./lib/main.ts";
import { RED, SUCCESS } from "./lib/runs.ts";
import { mergeWaiting } from "./merge-green.ts";

// RED_ISSUE is the title of the issue that stands open while main is red.
export const RED_ISSUE = "CI is failing on main";
// REQUIRED_CHECK is the check main requires of a pull request.
export const REQUIRED_CHECK = "All checks passed";

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
  const run: Run = { conclusion: requireEnv("CONCLUSION"), headSha: requireEnv("HEAD_SHA"), url: requireEnv("RUN_URL") };
  const state = await reportHealth(gitHubFor(requireEnv("ISSUES_TOKEN"), repository), run);
  if (state !== State.Green) {
    return;
  }
  const token = optionalEnv("GH_TOKEN");
  if (token === undefined) {
    warning("AUTOMERGE_TOKEN is not set; nothing merged (see auto-merge.yaml)");
    return;
  }
  await mergeWaiting(gitHubFor(token, repository), REQUIRED_CHECK);
}

if (import.meta.main) {
  await runMain(main);
}
