// The release plan run in shadow beside the release that runs now, step 3 of
// docs/adr/0016-one-release-commit.md's rollout: before release.ts releases
// anything, the plan of the same commit is made, and after, the tags it
// planned are set beside those the run cut, on the run's summary page. A
// tag the two disagree on is a warning. The plan writes nothing, pushes
// nothing and fails no release: a plan that fails is a warning too. A
// nested module the plan raises is released by the run that follows the
// require-limes pull request, so its tag is not compared here.

import { summarize, table, warning } from "./lib/actions.ts";
import { ROOT_MODULE } from "./lib/modules.ts";
import { planReleases, type Planner } from "./release-plan.ts";

// Agreement is what a row says of one tag.
const Agreement = {
  Same: "same",
  Differs: "differs",
  Later: "not released this run: a nested module follows limes in a later run",
  NotPlanned: "not planned",
} as const;

// moduleOf is the module a release tag is of: its prefix's, or the root's.
function moduleOf(tag: string): string {
  const slash = tag.lastIndexOf("/");
  return slash < 0 ? ROOT_MODULE : tag.slice(0, slash);
}

// ShadowReport is the rows of the summary's table, planned tag, released
// tag and their agreement, and each disagreement said in a line.
export interface ShadowReport {
  rows: string[][];
  mismatches: string[];
}

// shadowReport sets the tags planned beside the tags released, by module.
export function shadowReport(planned: readonly string[], released: readonly string[]): ShadowReport {
  const byModule = new Map(released.map((t) => [moduleOf(t), t]));
  const rows: string[][] = [];
  const mismatches: string[] = [];
  for (const tag of planned) {
    const module = moduleOf(tag);
    const cut = byModule.get(module);
    byModule.delete(module);
    if (cut === undefined) {
      rows.push([tag, "", Agreement.Later]);
    } else if (cut === tag) {
      rows.push([tag, cut, Agreement.Same]);
    } else {
      rows.push([tag, cut, Agreement.Differs]);
      mismatches.push(`${module}: planned ${tag}, released ${cut}`);
    }
  }
  for (const [module, cut] of byModule) {
    rows.push(["", cut, Agreement.NotPlanned]);
    mismatches.push(`${module}: released ${cut}, not planned`);
  }
  return { rows, mismatches };
}

// planInShadow is the tags the plan of the checked-out commit cuts, or
// undefined, with a warning, when the plan fails.
export async function planInShadow(p: Planner): Promise<string[] | undefined> {
  try {
    return (await planReleases(p)).map((m) => m.tag);
  } catch (err) {
    warning(`the release plan in shadow failed: ${err instanceof Error ? err.message : String(err)}`);
    return undefined;
  }
}

// SUMMARY_HEADER heads the summary's table.
const SUMMARY_HEADER = ["Planned", "Released", "Agreement"];

// reportShadow puts the report of planned beside released on the run's
// summary page, and warns of each disagreement.
export function reportShadow(planned: readonly string[], released: readonly string[]): void {
  const report = shadowReport(planned, released);
  for (const line of report.mismatches) {
    warning(`the release plan in shadow disagrees: ${line}`);
  }
  const body = report.rows.length === 0 ? "Nothing planned, nothing released." : table(SUMMARY_HEADER, report.rows);
  summarize(`### Release plan in shadow (ADR-0016)\n\n${body}`);
}
