// The CI workflow, and what the conclusions of its runs and jobs mean: one
// place for every command that reads them.

import type { GitHub } from "./github.ts";

// CI_WORKFLOW is the workflow whose runs say whether a commit passed: its
// push runs on main vouch for a release, and its latest run on main says
// whether main is green.
export const CI_WORKFLOW = ".github/workflows/ci.yaml";
// CI_WORKFLOW_NAME is CI_WORKFLOW's `name:`, which auto-merge.yaml's
// workflow_run trigger names it by. runs.test.ts pins both files to it.
export const CI_WORKFLOW_NAME = "CI";
// COMPLETED is the status of a run or job that has finished.
export const COMPLETED = "completed";
// SUCCESS is the conclusion of a run or job that ran and passed.
export const SUCCESS = "success";
// PASSING are the conclusions of a job that pass: one the change did not
// reach is skipped, which is a pass.
export const PASSING: readonly string[] = [SUCCESS, "skipped"];
// RED are the conclusions of a run that leave its branch broken. A
// cancelled run says nothing about the code.
export const RED: readonly string[] = ["failure", "timed_out", "startup_failure"];
// REQUIRED_CHECK is the `name:` of ci.yaml's checks job, the one check main's
// ruleset requires, which stands for every job a merge needs.
// repo-settings.test.ts holds it to ci.yaml.
export const REQUIRED_CHECK = "All checks passed";

// requiredConclusion is what a CI run says about its branch: how its
// REQUIRED_CHECK ended, or the run's own conclusion when that job did not
// finish — a run that never started it, or was cancelled before it. A job
// outside the check, such as a benchmark, fails the run without breaking the
// branch.
export async function requiredConclusion(github: Pick<GitHub, "runJobs">, run: { id: number; conclusion: string }): Promise<string> {
  const check = (await github.runJobs(run.id)).filter((j) => j.name === REQUIRED_CHECK).at(-1);
  return check?.status === COMPLETED && check.conclusion !== undefined ? check.conclusion : run.conclusion;
}
