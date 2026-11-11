// The shared task library's tasks this repository replaces or leaves out,
// and why each must stay so (Taskfile.yaml, includes.go).

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { test } from "node:test";

import { parse } from "yaml";

// ROOT is the repository's root.
const ROOT = join(import.meta.dirname, "..", "..");
const TASKFILE = join(ROOT, "Taskfile.yaml");
// LIBRARY is the include of the shared go library.
const LIBRARY = "go";
// GOIMPORTS_INPUT is the library's switch for goimports in its fmt, and
// GOIMPORTS_NEVER its value for gofmt alone. goimports adds an import for a
// name it cannot place, and a package-level variable of another test file is
// one: three pgstore tests got gioui.org/app.
const GOIMPORTS_INPUT = "GO_FMT_GOIMPORTS";
const GOIMPORTS_NEVER = "0";
// TOOL_MODFILE_INPUT names the go.mod whose tool directives the library runs
// its tools from, before PATH; TOOLS_MODFILE_VAR is this repository's.
const TOOL_MODFILE_INPUT = "GO_TOOL_MODFILE";
const TOOLS_MODFILE_VAR = "TOOLS_MODFILE";
// GOVULNCHECK is the tool the library's vuln runs, as tools/go.mod pins it.
const GOVULNCHECK = "golang.org/x/vuln/cmd/govulncheck";
// GOLANGCI_LINT_MODFILE_VAR names the module that pins golangci-lint, apart
// from the one the library reads.
const GOLANGCI_LINT_MODFILE_VAR = "GOLANGCI_LINT_MODFILE";

interface Taskfile {
  vars: Record<string, string>;
  includes: Record<string, { excludes?: string[]; checksum?: string; vars?: Record<string, string> }>;
  tasks: Record<string, { cmds?: (string | { cmd?: string })[] }>;
}
const taskfile = parse(readFileSync(TASKFILE, "utf8")) as Taskfile;
const library = taskfile.includes[LIBRARY];

test("the library's lint and its build are not this repository's", () => {
  const excluded = library?.excludes ?? [];
  for (const task of ["lint", "build"]) {
    assert.ok(excluded.includes(task), `the library's ${task} is not excluded`);
  }
  const lint = (taskfile.tasks.lint?.cmds ?? []).map((c) => (typeof c === "string" ? c : (c.cmd ?? "")));
  assert.ok(lint.some((c) => c.includes(`-modfile={{.${GOLANGCI_LINT_MODFILE_VAR}}} golangci-lint run`)), "lint does not run the pinned golangci-lint");
});

test("fmt is the library's, with goimports never run", () => {
  assert.equal(library?.vars?.[GOIMPORTS_INPUT], GOIMPORTS_NEVER);
  assert.ok(!(library?.excludes ?? []).includes("fmt"), "the library's fmt is excluded");
  assert.equal(taskfile.tasks.fmt, undefined, "fmt is written here as well");
});

test("vuln is the library's, run from the tools module that pins govulncheck", () => {
  assert.equal(library?.vars?.[TOOL_MODFILE_INPUT], `{{.${TOOLS_MODFILE_VAR}}}`);
  assert.equal(taskfile.tasks.vuln, undefined, "vuln is written here as well");
  // A tool the module does not pin is taken from PATH by the library.
  const tools = readFileSync(join(ROOT, taskfile.vars[TOOLS_MODFILE_VAR] ?? ""), "utf8");
  assert.ok(tools.includes(GOVULNCHECK), `${taskfile.vars[TOOLS_MODFILE_VAR]} does not pin ${GOVULNCHECK}`);
});

// That each include loads from the lock is tasklib:check, in verify:gate.
test("the shared library is loaded from the committed lock, never fetched", () => {
  const rc = parse(readFileSync(join(ROOT, ".taskrc.yml"), "utf8")) as { remote?: { offline?: boolean } };
  assert.equal(rc.remote?.offline, true, ".taskrc.yml does not set remote.offline");
  for (const name of Object.keys(taskfile.includes)) {
    // The lock is the whole of it: a checksum beside it is a second one that
    // tasklib:bump does not move.
    assert.equal(taskfile.includes[name]?.checksum, undefined, `the ${name} include carries a checksum of its own`);
  }
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
// remote Taskfile, which a clone sometimes passes when the lock is refreshed.
const TASK_DEFAULT_REMOTE_TIMEOUT_SECONDS = 10;
// DURATION is a Go duration of one unit: what .taskrc.yml's timeout reads.
const DURATION = /^(\d+)(s|m|h)$/;
const SECONDS_PER: Record<string, number> = { s: 1, m: 60, h: 3600 };

test("the shared library's fetch may take longer than Task's default", () => {
  const rc = parse(readFileSync(join(ROOT, ".taskrc.yml"), "utf8")) as { remote?: { timeout?: string } };
  const match = DURATION.exec(rc.remote?.timeout ?? "");
  assert.ok(match, `remote.timeout ${JSON.stringify(rc.remote?.timeout)} is no duration of one unit`);
  const [, amount = "", unit = ""] = match;
  assert.ok(Number(amount) * (SECONDS_PER[unit] ?? 0) > TASK_DEFAULT_REMOTE_TIMEOUT_SECONDS, `remote.timeout ${rc.remote?.timeout} is not above Task's default`);
});
