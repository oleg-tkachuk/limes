import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { dirname, join, normalize } from "node:path";
import { after, before, describe, test } from "node:test";

import { parse } from "yaml";

import { ALL_GROUPS, Group, INERT, READ_BY_TESTS, OUTPUT_CODE, PUSH_EVENT, SCRIPTS_TASKS, VERIFY_GATE, candidateRange, checkedGroups, classify, reachedGroups, reachesCode, verifyTasks } from "./changes.ts";
import { globToRegExp } from "./lib/glob.ts";
import { tempRepo, type TempRepo } from "./lib/temp-repo.ts";

// CODEQL is the workflow whose paths-ignore must be INERT.
const CODEQL = new URL("../workflows/codeql.yaml", import.meta.url);
// CI is the workflow whose changes job runs this script.
const CI = new URL("../workflows/ci.yaml", import.meta.url);

describe("reachesCode", () => {
  const cases: [string, string[], boolean][] = [
    ["no change", [], false],
    ["the README", ["README.md"], false],
    ["a document", ["docs/diagrams.md"], false],
    // Only documentation's own file types are inert under docs/.
    ["Go source under docs", ["docs/example.go"], true],
    ["the licence", ["LICENSE"], false],
    ["a licence below the root", ["pgstore/LICENSE"], true],
    ["documentation and code", ["README.md", "verifier.go"], true],
    ["Go source", ["verifier.go"], true],
    ["a fixture", ["testdata/golden_token.jwt"], true],
    ["a workflow", [".github/workflows/ci.yaml"], true],
    ["go.mod", ["go.mod"], true],
    ["a file type nobody listed", ["something.new"], true],
    ["an image in a directory", ["docs/img/flow.svg"], false],
  ];
  for (const [name, paths, want] of cases) {
    test(name, () => assert.equal(reachesCode(paths), want));
  }
});

describe("globToRegExp", () => {
  const cases: [string, string, boolean][] = [
    ["**/*.md", "README.md", true],
    ["**/*.md", "a/b/c.md", true],
    ["**/*.md", "a.md.go", false],
    ["*.md", "docs/a.md", false],
    ["LICENSE", "LICENSE", true],
    ["LICENSE", "xLICENSE", false],
    [".gitignore", "agitignore", false],
  ];
  for (const [pattern, path, want] of cases) {
    test(`${pattern} against ${path}`, () => assert.equal(globToRegExp(pattern).test(path), want));
  }
});

test("codeql.yaml ignores exactly the inert paths", () => {
  const workflow = parse(readFileSync(CODEQL, "utf8"));
  assert.deepEqual([...workflow.on.push["paths-ignore"]].sort(), [...INERT].sort());
  assert.deepEqual([...workflow.on.pull_request["paths-ignore"]].sort(), [...INERT].sort());
});

// A push run vouches for releases and says whether main is green, so it
// checks everything; a pull request checks what its diff reaches.
describe("checkedGroups", () => {
  test("a push checks every group, even for documentation", () => assert.deepEqual([...checkedGroups(PUSH_EVENT, new Set())].sort(), [...ALL_GROUPS].sort()));
  test("a push of the scripts checks every group", () =>
    assert.deepEqual([...checkedGroups(PUSH_EVENT, new Set([Group.Scripts]))].sort(), [...ALL_GROUPS].sort()));
  test("a pull request checks what it reaches", () => assert.deepEqual([...checkedGroups("pull_request", new Set([Group.Pgstore]))], [Group.Pgstore]));
  test("a pull request of documentation checks nothing", () => assert.equal(checkedGroups("pull_request", new Set()).size, 0));
});

test("ci.yaml tells the changes step its event and dispatches on the diff", () => {
  const ci = parse(readFileSync(CI, "utf8")) as {
    jobs: Record<string, { outputs?: Record<string, string>; steps?: { id?: string; env?: Record<string, string> }[]; if?: string }>;
  };
  const step = ci.jobs.changes?.steps?.find((s) => s.id === "filter");
  assert.equal(step?.env?.EVENT, "${{ github.event_name }}");
  assert.ok(ci.jobs.changes?.outputs?.[OUTPUT_CODE] !== undefined);
  assert.match(ci.jobs["dispatch-release"]?.if ?? "", new RegExp(`needs\\.changes\\.outputs\\.${OUTPUT_CODE} == 'true'`));
});

test("a push that created the branch names no diff", () => {
  assert.equal(candidateRange({ before: "0".repeat(40), after: "abc" }), undefined);
});

describe("classify", () => {
  let repo: TempRepo;
  const sha: Record<string, string> = {};

  before(async () => {
    repo = await tempRepo();
    repo.write("m.go", "package m\n");
    sha.base = await repo.commit("base");
    repo.write("docs/guide.md", "note\n");
    sha.docs = await repo.commit("docs");
    repo.write("m.go", "package m // more\n");
    sha.code = await repo.commit("code");
    repo.write(".github/workflows/ci.yaml", "name: ci\n");
    sha.workflow = await repo.commit("workflow");
    await repo.run("mv", ".github/workflows/ci.yaml", "docs/ci.yaml");
    sha.moved = await repo.commit("moved");
    await repo.run("mv", "m.go", "m.md");
    sha.renamed = await repo.commit("renamed");
  });
  after(() => repo.cleanup());

  const cases: [string, () => Parameters<typeof classify>[0], boolean][] = [
    ["a pull request with documentation only", () => ({ base: sha.base, head: sha.docs }), false],
    // Renamed into an inert name, the code it was is still a change.
    ["code renamed to documentation", () => ({ base: sha.moved, head: sha.renamed }), true],
    ["a workflow moved into docs", () => ({ base: sha.workflow, head: sha.moved }), true],
    ["a pull request with code", () => ({ base: sha.base, head: sha.code }), true],
    ["a push of documentation only", () => ({ before: sha.base, after: sha.docs }), false],
    ["a push of code", () => ({ before: sha.docs, after: sha.code }), true],
    ["a pull request's ends win over a push's", () => ({ base: sha.base, head: sha.docs, before: sha.docs, after: sha.code }), false],
    ["a push that created the branch", () => ({ before: "0".repeat(40), after: sha.docs }), true],
    ["a base this clone does not have", () => ({ before: "1".repeat(40), after: sha.docs }), true],
    ["a run by hand, with no diff", () => ({ after: sha.docs }), true],
  ];
  for (const [name, env, want] of cases) {
    test(name, async () => assert.equal((await classify(env(), repo.dir)).size > 0, want));
  }
  test("no diff reaches every group", async () => assert.deepEqual([...(await classify({ after: sha.docs }, repo.dir))].sort(), [...ALL_GROUPS].sort()));
});

// A file of INERT that a test reads is no documentation to the checks: a
// change to it must run that test.
describe("paths a test reads", () => {
  test("the README reaches the examples, whose test runs its walkthrough", () =>
    assert.deepEqual([...reachedGroups(["README.md"])], [Group.Examples]));
  test("other documentation still reaches nothing", () => assert.equal(reachedGroups(["docs/guide.md", "pgstore/README.md"]).size, 0));
  test("every inert path a Go test names is listed", () => {
    const root = join(import.meta.dirname, "..", "..");
    const tests = execFileSync("git", ["ls-files", "*_test.go"], { cwd: root, encoding: "utf8" }).split("\n").filter(Boolean);
    const read = new Set<string>();
    for (const file of tests) {
      for (const [, literal] of readFileSync(join(root, file), "utf8").matchAll(/"([^"\s]+\.[A-Za-z0-9]+)"/g)) {
        const path = normalize(join(dirname(file), literal ?? ""));
        if (!path.startsWith("..") && existsSync(join(root, path)) && INERT.some((g) => globToRegExp(g).test(path))) {
          read.add(path);
        }
      }
    }
    assert.ok(read.size > 0, "no Go test reads an inert path: this check found nothing");
    for (const path of read) {
      assert.ok(READ_BY_TESTS[path] !== undefined, `${path} is read by a test and inert to the checks`);
    }
  });
});

describe("reachedGroups", () => {
  const cases: [string, string[], Group[]][] = [
    ["documentation", ["docs/guide.md", "pgstore/README.md"], []],
    ["the root module's code", ["verifier.go"], [Group.Root]],
    ["a root fixture", ["testdata/golden_token.jwt"], [Group.Root]],
    ["pgstore", ["pgstore/charge.go"], [Group.Pgstore]],
    ["redisreplay", ["redisreplay/seen.lua"], [Group.Redisreplay]],
    ["otelmetric", ["otelmetric/store.go"], [Group.Otelmetric]],
    ["an example", ["examples/quickstart/main.go"], [Group.Examples]],
    ["a CI script", [".github/ci/changes.ts"], [Group.Scripts]],
    ["the release tooling", [".github/release/modules.cjs"], [Group.Scripts]],
    ["two modules", ["pgstore/a.go", "otelmetric/b.go"], [Group.Otelmetric, Group.Pgstore]],
    // What decides what CI runs reaches every check.
    ["a workflow", [".github/workflows/ci.yaml"], [...ALL_GROUPS]],
    ["the setup action", [".github/actions/setup-ci/action.yml"], [...ALL_GROUPS]],
    ["the cache action", [".github/actions/go-cache/action.yml"], [...ALL_GROUPS]],
    ["the Taskfile", ["Taskfile.yaml"], [...ALL_GROUPS]],
    ["a pinned tool", ["tools/go.mod"], [...ALL_GROUPS]],
    ["the root go.mod", ["go.mod"], [...ALL_GROUPS]],
    ["the linter's configuration", [".golangci.yml"], [...ALL_GROUPS]],
    ["a file type nobody listed", ["something.new"], [Group.Root]],
  ];
  for (const [name, paths, want] of cases) {
    test(name, () => assert.deepEqual([...reachedGroups(paths)].sort(), [...want].sort()));
  }
});

describe("verifyTasks", () => {
  test("the scripts alone run the scripts' part", () => assert.equal(verifyTasks(new Set([Group.Scripts])), SCRIPTS_TASKS));
  test("anything more runs the whole gate", () => assert.equal(verifyTasks(new Set([Group.Scripts, Group.Root])), VERIFY_GATE));
  test("a nested module runs the whole gate", () => assert.equal(verifyTasks(new Set([Group.Pgstore])), VERIFY_GATE));
  // The Verify job splits the value into task names: it is only ever one of
  // the two fixed strings.
  test("only ever one of the two fixed task lists", () => {
    const all = [new Set<Group>(), ...ALL_GROUPS.map((g) => new Set([g])), new Set(ALL_GROUPS)];
    for (const groups of all) {
      assert.ok([SCRIPTS_TASKS, VERIFY_GATE].includes(verifyTasks(groups)));
    }
  });
});
