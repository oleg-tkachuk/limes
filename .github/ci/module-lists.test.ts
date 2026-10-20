// The module table, .github/release/modules.cjs, is the one list of the
// modules this repository releases. The places that cannot import it — the
// workflows, the Taskfile — and the ones that name its modules for a purpose
// of their own each repeat part of it; these tests fail when one falls
// behind the table.

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { test } from "node:test";

import { parse } from "yaml";

import { GROUP_DIRS, Group } from "./changes.ts";
import { ROOT_MODULE, releasedModules, selectModule } from "./lib/modules.ts";
import { CI_WORKFLOW } from "./lib/runs.ts";
import { GATED } from "./release.ts";

const ROOT = join(import.meta.dirname, "..", "..");
const TASKFILE = "Taskfile.yaml";
// CHANGES_JOB is ci.yaml's job that sets one output per group.
const CHANGES_JOB = "changes";
// LINT_ALL and VERIFY_GATE are the Taskfile tasks that name each module.
const LINT_ALL = "lint:all";
const VERIFY_GATE = "verify:gate";
// TASK_VAR is how a Taskfile list names one of its vars.
const TASK_VAR = /^\{\{\.([A-Z_]+)\}\}$/;

const read = (path: string): unknown => parse(readFileSync(join(ROOT, path), "utf8"));

// nested is every module of the table but the root, with its directory.
const nested = releasedModules(ROOT)
  .filter((name) => name !== ROOT_MODULE)
  .map((name) => selectModule(ROOT, name));

interface Task {
  cmds?: ({ task?: string; for?: string[] } | string)[];
}
const taskfile = read(TASKFILE) as { vars: Record<string, string>; tasks: Record<string, Task> };

test("the table has nested modules to check", () => {
  assert.ok(nested.length > 0);
});

test("every nested module is a change group of its own", () => {
  const dirs = GROUP_DIRS as Readonly<Record<string, readonly string[]>>;
  for (const m of nested) {
    assert.deepEqual(dirs[m.name], [`${m.dir}/`], `changes.ts has no group for ${m.name}`);
  }
});

test("ci.yaml's changes job sets an output for every group", () => {
  const ci = read(CI_WORKFLOW) as { jobs: Record<string, { outputs?: Record<string, string> }> };
  const outputs = Object.keys(ci.jobs[CHANGES_JOB]?.outputs ?? {});
  for (const group of Object.values(Group)) {
    assert.ok(outputs.includes(group), `no output ${group}`);
  }
});

test("every nested module's release is gated", () => {
  assert.deepEqual([...GATED].sort(), nested.map((m) => m.name).sort());
});

test(`${LINT_ALL} lints every nested module`, () => {
  const linted = (taskfile.tasks[LINT_ALL]?.cmds ?? []).flatMap((c) => (typeof c === "string" ? [] : (c.for ?? [])));
  const dirs = linted.map((item) => {
    const name = TASK_VAR.exec(item)?.[1];
    return name === undefined ? item : taskfile.vars[name];
  });
  for (const m of nested) {
    assert.ok(dirs.includes(m.dir), `${LINT_ALL} does not lint ${m.dir}`);
  }
});

test(`${VERIFY_GATE} runs every nested module's own gate`, () => {
  const tasks = (taskfile.tasks[VERIFY_GATE]?.cmds ?? []).flatMap((c) => (typeof c === "string" || c.task === undefined ? [] : [c.task]));
  for (const m of nested) {
    assert.ok(tasks.includes(`verify:${m.name}`), `${VERIFY_GATE} does not run verify:${m.name}`);
  }
});
