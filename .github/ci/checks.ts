// The one check main requires: every job the required check needs passed or
// was skipped.
//
// Usage: NEEDS=<toJSON(needs)> node checks.ts
//
// A skipped job was not reached by the change, which is a pass; a failure, a
// cancellation, or a job that never reported, is not. The jobs and their
// results go on the run's summary page, and a failure names the jobs that
// failed.

import { info, summarize, table } from "./lib/actions.ts";
import { requireEnv } from "./lib/env.ts";
import { runMain } from "./lib/main.ts";

// PASSING are the results of a job that let the required check pass.
const PASSING: readonly string[] = ["success", "skipped"];
// SUMMARY_HEADER heads the summary table.
const SUMMARY_HEADER = ["Job", "Result"];

// Needs is the needs context of a job, as toJSON prints it.
export type Needs = Record<string, { result?: string }>;

// failedJobs is each job of needs whose result does not pass, with it.
export function failedJobs(needs: Needs): string[] {
  return Object.entries(needs)
    .filter(([, job]) => !PASSING.includes(job.result ?? ""))
    .map(([id, job]) => `${id} (${job.result ?? "no result"})`);
}

// parseNeeds reads the needs context, refusing anything but an object of
// jobs.
export function parseNeeds(json: string): Needs {
  const parsed: unknown = JSON.parse(json);
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed) || Object.keys(parsed).length === 0) {
    throw new Error("NEEDS is not the needs of a job");
  }
  return parsed as Needs;
}

async function main(): Promise<void> {
  const needs = parseNeeds(requireEnv("NEEDS"));
  summarize(`### All checks\n\n${table(SUMMARY_HEADER, Object.entries(needs).map(([id, job]) => [id, job.result ?? "no result"]))}`);
  const failed = failedJobs(needs);
  if (failed.length > 0) {
    throw new Error(`a required job did not pass: ${failed.join(", ")}`);
  }
  info(`every required job passed or was skipped: ${Object.keys(needs).join(", ")}`);
}

if (import.meta.main) {
  await runMain(main);
}
