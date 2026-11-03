// The shared task library's tasks this repository replaces or leaves out,
// and why each must stay so (Taskfile.yaml, includes.go).

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { test } from "node:test";

import { parse } from "yaml";

const TASKFILE = join(import.meta.dirname, "..", "..", "Taskfile.yaml");
// LIBRARY is the include of the shared go library.
const LIBRARY = "go";
// GOIMPORTS adds an import for a name it cannot place, and a package-level
// variable of another test file is one: three pgstore tests got gioui.org/app.
const GOIMPORTS = "goimports";

interface Taskfile {
  includes: Record<string, { excludes?: string[] }>;
  tasks: Record<string, { cmds?: (string | { cmd?: string })[] }>;
}
const taskfile = parse(readFileSync(TASKFILE, "utf8")) as Taskfile;

test("the library's tools from PATH, its fmt and its build are not this repository's", () => {
  const excluded = taskfile.includes[LIBRARY]?.excludes ?? [];
  for (const task of ["vuln", "lint", "fmt", "build"]) {
    assert.ok(excluded.includes(task), `the library's ${task} is not excluded`);
  }
});

test("fmt is gofmt alone, never goimports", () => {
  const cmds = (taskfile.tasks.fmt?.cmds ?? []).map((c) => (typeof c === "string" ? c : (c.cmd ?? "")));
  assert.ok(cmds.some((c) => c.startsWith("gofmt -s -w")), "fmt does not run gofmt -s -w");
  assert.ok(!cmds.some((c) => c.includes(GOIMPORTS)), "fmt runs goimports");
});

// The weekly workflow runs the soak as test:pgstore:soak does, without Task.
test("the weekly soak runs the command test:pgstore:soak runs", () => {
  const workflow = parse(readFileSync(join(import.meta.dirname, "..", "workflows", "bench.yaml"), "utf8")) as {
    jobs: { soak: { steps: { name?: string; run?: string; "working-directory"?: string }[] } };
  };
  const step = workflow.jobs.soak.steps.find((s) => s.name === "Soak");
  const task = (taskfile.tasks["test:pgstore:soak"]?.cmds ?? []).map((c) => (typeof c === "string" ? c : (c.cmd ?? "")));
  assert.deepEqual([step?.run], task);
  assert.equal(step?.["working-directory"], "pgstore");
});

// TASK_DEFAULT_REMOTE_TIMEOUT_SECONDS is Task's own bound on fetching a
// remote Taskfile, which a clone from a CI runner sometimes passes.
const TASK_DEFAULT_REMOTE_TIMEOUT_SECONDS = 10;
// DURATION is a Go duration of one unit: what .taskrc.yml's timeout reads.
const DURATION = /^(\d+)(s|m|h)$/;
const SECONDS_PER: Record<string, number> = { s: 1, m: 60, h: 3600 };

test("the shared library's fetch may take longer than Task's default", () => {
  const rc = parse(readFileSync(join(import.meta.dirname, "..", "..", ".taskrc.yml"), "utf8")) as { remote?: { timeout?: string } };
  const match = DURATION.exec(rc.remote?.timeout ?? "");
  assert.ok(match, `remote.timeout ${JSON.stringify(rc.remote?.timeout)} is no duration of one unit`);
  const [, amount = "", unit = ""] = match;
  assert.ok(Number(amount) * (SECONDS_PER[unit] ?? 0) > TASK_DEFAULT_REMOTE_TIMEOUT_SECONDS, `remote.timeout ${rc.remote?.timeout} is not above Task's default`);
});
