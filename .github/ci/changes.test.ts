import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { after, before, describe, test } from "node:test";

import { parse } from "yaml";

import { INERT, candidateRange, classify, reachesCode } from "./changes.ts";
import { globToRegExp } from "./lib/glob.ts";
import { tempRepo, type TempRepo } from "./lib/temp-repo.ts";

// CODEQL is the workflow whose paths-ignore must be INERT.
const CODEQL = new URL("../workflows/codeql.yaml", import.meta.url);

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
    test(name, async () => assert.equal(await classify(env(), repo.dir), want));
  }
});
