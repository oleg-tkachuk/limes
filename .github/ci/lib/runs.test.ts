import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

import { parse } from "yaml";

import { CI_WORKFLOW, CI_WORKFLOW_NAME, PASSING, RED } from "./runs.ts";

// AUTO_MERGE_WORKFLOW runs on the end of every CI run, which it names by the
// workflow's name.
const AUTO_MERGE_WORKFLOW = ".github/workflows/auto-merge.yaml";

const workflow = (path: string): unknown => parse(readFileSync(new URL(`../../../${path}`, import.meta.url), "utf8"));

// A rename of ci.yaml that auto-merge.yaml does not follow leaves merging and
// the watch over main dead, with nothing said.
test("auto-merge.yaml runs on the end of ci.yaml's runs", () => {
  const ci = workflow(CI_WORKFLOW) as { name?: string };
  assert.equal(ci.name, CI_WORKFLOW_NAME);
  const autoMerge = workflow(AUTO_MERGE_WORKFLOW) as { on?: { workflow_run?: { workflows?: string[] } } };
  assert.deepEqual(autoMerge.on?.workflow_run?.workflows, [CI_WORKFLOW_NAME]);
});

test("a red run is never a passing one", () => {
  assert.equal(
    RED.some((c) => PASSING.includes(c)),
    false,
  );
});
