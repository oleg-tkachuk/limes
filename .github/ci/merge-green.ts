// Merge the pull requests whose CI run just passed, at the head that run
// tested, unless one of them is a person's to merge.
//
// Usage: GH_TOKEN=… REPO=owner/name HEAD_SHA=<sha> PULL_REQUESTS=<json> node merge-green.ts
//
// auto-merge.yaml runs this from main when ci.yaml completes green for a pull
// request: HEAD_SHA is the commit that run tested and PULL_REQUESTS the
// workflow_run's pull_requests array, [{"number": 7, …}]. Each pull request
// is read and decided now, at merge time, and merged only at HEAD_SHA: a push
// since the run moves the head, the merge is refused, and the new head's own
// run decides it. Nothing is armed ahead of time, so there is no earlier
// verdict for a later push to merge under.
//
// Left for a person (merge-policy.ts): a draft — mark a pull request draft to
// hold it —, a fork's, Renovate's, one into another branch than BASE_BRANCH,
// one whose author cannot push here, and one that changes CI_DEFINITION,
// whose green check is no evidence. Its files are those of HEAD_SHA, read
// through the compare API as GitHub lists them, not from a diff's headers.
//
// While main's own newest CI run failed, nothing is merged: two pull requests
// each green alone can break main together, and merging more onto a broken
// main hides which change broke it. Each pull request held so is merged once
// main is green again (main-health.ts). What each run decided goes on its
// summary page.
//
// Without GH_TOKEN it warns and does nothing: a merge made with the
// workflow's own GITHUB_TOKEN would start no CI on main (auto-merge.yaml).

import { notice, summarize, table, warning } from "./lib/actions.ts";
import { optionalEnv, requireEnv } from "./lib/env.ts";
import { gitHubFor, type GitHub, type PullRequest, type PullRequestRun } from "./lib/github.ts";
import { runMain } from "./lib/main.ts";
import { CI_WORKFLOW, COMPLETED, RED, SUCCESS } from "./lib/runs.ts";
import { BASE_BRANCH, canPush, changesCi, filesListedInFull, isRenovate, nestedModuleChange } from "./merge-policy.ts";

// COMMIT_ID is a full commit id, the only HEAD_SHA a run passes.
const COMMIT_ID = /^[0-9a-f]{40}$/;

// Decision is what was done with one pull request, and why.
export interface Decision {
  number: number;
  merged: boolean;
  note: string;
}

// SUMMARY_HEADER heads the summary table.
const SUMMARY_HEADER = ["Pull request", "Merged", "Why"];

// summarizeDecisions puts decisions on the run's summary page.
export function summarizeDecisions(title: string, decisions: readonly Decision[]): void {
  summarize(`### ${title}\n\n${table(SUMMARY_HEADER, decisions.map((d) => [`#${d.number}`, d.merged ? "yes" : "no", d.note]))}`);
}

// mainIsRed is why merges wait for main, or undefined while main's newest
// finished CI run passed.
export async function mainIsRed(github: GitHub): Promise<string | undefined> {
  const run = await github.latestRunOf(CI_WORKFLOW, BASE_BRANCH);
  if (run === undefined || !RED.includes(run.conclusion)) {
    return undefined;
  }
  return `${BASE_BRANCH} is red at ${run.headSha.slice(0, 7)} (${run.url}); merging waits until it is green`;
}

// heldBack is why a pull request is not this run's to merge, or undefined
// when it may be merged once its files are read.
export function heldBack(pr: PullRequest, headSha: string): string | undefined {
  if (!pr.open || pr.draft || pr.fromFork) {
    return `#${pr.number} is not an open same-repository pull request ready to merge; left alone`;
  }
  if (isRenovate(pr.author)) {
    return `#${pr.number} is Renovate's, which merges its own; left alone`;
  }
  if (pr.baseRef !== BASE_BRANCH) {
    return `#${pr.number} merges into ${pr.baseRef}, not ${BASE_BRANCH}; left for a person to merge`;
  }
  if (pr.headSha !== headSha) {
    return `#${pr.number} has moved past the commit this run tested; its own run decides it`;
  }
  return undefined;
}

// ownRun is the run that decides pr: the newest pull_request run of CI at its
// head that ran for it into main. A green check of the same name at the same
// commit — another pull request's run into another base, another app's — is
// not its verdict.
export async function ownRun(github: GitHub, pr: PullRequest): Promise<PullRequestRun | undefined> {
  const runs = await github.pullRequestRuns(CI_WORKFLOW, pr.headSha);
  return runs.find((r) => r.pullRequests.some((p) => p.number === pr.number && p.baseRef === BASE_BRANCH));
}

// unpassed is why pr's own run does not let it merge, or undefined when it
// passed.
export function unpassed(pr: PullRequest, run: PullRequestRun | undefined): string | undefined {
  if (run === undefined) {
    return `#${pr.number} has no CI run of its own into ${BASE_BRANCH} at ${pr.headSha}; left alone`;
  }
  if (run.status !== COMPLETED) {
    return `#${pr.number}'s newest run is still going; its end decides it`;
  }
  if (run.conclusion !== SUCCESS) {
    return `#${pr.number}'s own run ended as ${run.conclusion ?? "nothing"}; left alone`;
  }
  return undefined;
}

// mergeIfGreen merges pull request number at headSha if its own CI run passed
// and nothing leaves it for a person, and says why not otherwise. The merge
// commit's body is the title that run checked, not the title now: one
// edited since has a run of its own still to pass.
export async function mergeIfGreen(github: GitHub, number: number, headSha: string): Promise<Decision> {
  const decided = (merged: boolean, note: string): Decision => {
    notice(note);
    return { number, merged, note };
  };
  const pr = await github.pullRequest(number);
  const held = heldBack(pr, headSha);
  if (held !== undefined) {
    return decided(false, held);
  }
  const run = await ownRun(github, pr);
  const failed = unpassed(pr, run);
  if (failed !== undefined || run === undefined) {
    return decided(false, failed ?? "");
  }
  if (!(await canPush(github, pr.author.login))) {
    return decided(false, `#${number} is by ${pr.author.login}, who cannot push here; left for a person to merge`);
  }
  // The files of headSha itself, the commit the run tested and the merge
  // names: the pull request's own listing is of its head now, which may have
  // moved and come back between the reads.
  const { files } = await github.compare(BASE_BRANCH, headSha);
  // Every file the pull request changes must have been listed: one the API
  // left out could be a CI definition.
  if (!filesListedInFull(files.length, pr.changedFiles)) {
    return decided(false, `#${number} changes ${pr.changedFiles ?? "an unknown number of"} files and ${files.length} were listed; left for a person to merge`);
  }
  const ci = changesCi(files);
  if (ci !== undefined) {
    return decided(false, `#${number} changes ${ci}, which decides what CI runs; left for a person to merge`);
  }
  const nested = await nestedModuleChange(github, files, BASE_BRANCH, headSha);
  if (nested !== undefined) {
    return decided(false, `#${number} changes ${nested} beyond its requirements, which decides what CI tests; left for a person to merge`);
  }
  // Refused if the head moved since it was read: the run tested headSha,
  // whose files are the ones read above.
  try {
    await github.merge(number, headSha, run.title);
    return decided(true, `#${number} merged at ${headSha}`);
  } catch (err) {
    const note = `#${number} could not be merged at ${headSha}; left as it is: ${err instanceof Error ? err.message : String(err)}`;
    warning(note);
    return { number, merged: false, note };
  }
}

// pullRequestNumbers reads the workflow_run's pull_requests, refusing any
// entry whose number is not one.
export function pullRequestNumbers(json: string): number[] {
  const parsed: unknown = JSON.parse(json);
  if (!Array.isArray(parsed)) {
    throw new Error("PULL_REQUESTS is not a JSON array");
  }
  return parsed.map((entry: unknown) => {
    const number = typeof entry === "object" && entry !== null && "number" in entry ? entry.number : undefined;
    if (typeof number !== "number" || !Number.isSafeInteger(number) || number <= 0) {
      throw new Error(`pull request number ${JSON.stringify(number)} is not a number`);
    }
    return number;
  });
}

// mergeGreen decides every pull request of the run, in turn, unless main is
// red.
export async function mergeGreen(github: GitHub, headSha: string, pullRequests: string): Promise<Decision[]> {
  if (!COMMIT_ID.test(headSha)) {
    throw new Error(`HEAD_SHA ${headSha} is not a commit id`);
  }
  const numbers = pullRequestNumbers(pullRequests);
  const red = await mainIsRed(github);
  const decisions: Decision[] = [];
  for (const number of numbers) {
    if (red !== undefined) {
      notice(`#${number}: ${red}`);
      decisions.push({ number, merged: false, note: red });
    } else {
      decisions.push(await mergeIfGreen(github, number, headSha));
    }
  }
  summarizeDecisions("Merge", decisions);
  return decisions;
}

// mergeWaiting merges every open pull request whose own CI run passed at its
// head: those a red main held back, once it is green again. One whose run did
// not pass is left out of the summary: its own run's end decides it.
export async function mergeWaiting(github: GitHub): Promise<Decision[]> {
  const decisions: Decision[] = [];
  if ((await mainIsRed(github)) !== undefined) {
    return decisions;
  }
  for (const open of await github.openPullRequestsInto(BASE_BRANCH)) {
    const pr = await github.pullRequest(open.number);
    if (unpassed(pr, await ownRun(github, pr)) === undefined) {
      decisions.push(await mergeIfGreen(github, pr.number, open.headSha));
    }
  }
  summarizeDecisions("Merge what waited for main", decisions);
  return decisions;
}

async function main(): Promise<void> {
  const token = optionalEnv("GH_TOKEN");
  if (token === undefined) {
    warning("AUTOMERGE_TOKEN is not set; nothing merged (see auto-merge.yaml)");
    return;
  }
  await mergeGreen(gitHubFor(token, requireEnv("REPO")), requireEnv("HEAD_SHA"), requireEnv("PULL_REQUESTS"));
}

if (import.meta.main) {
  await runMain(main);
}
