import assert from "node:assert/strict";
import { describe, test } from "node:test";

import { changed } from "./lib/fake-github.ts";
import { shellPatternToRegExp } from "./lib/glob.ts";
import { changesCi, definesCi, filesListedInFull, isRenovate, pathsOf } from "./merge-policy.ts";

describe("definesCi", () => {
  const cases: [string, boolean][] = [
    [".github/workflows/ci.yaml", true],
    [".github/ci/lib/git.ts", true],
    ["Taskfile.yaml", true],
    ["taskfile.yml", true],
    ["go.mod", true],
    ["pgstore/go.mod", false],
    ["pgstore/go.work", true],
    ["a/b/vendor/x", true],
    ["verifier.go", false],
    ["docs/renovate.json", false],
  ];
  for (const [path, want] of cases) {
    test(path, () => assert.equal(definesCi(path), want));
  }
});

describe("shellPatternToRegExp", () => {
  test("* crosses /", () => assert.ok(shellPatternToRegExp(".github/*").test(".github/a/b")));
  test("a class", () => assert.ok(shellPatternToRegExp("[Tt]askfile.*").test("taskfile.yml")));
  test("? is one character", () => assert.equal(shellPatternToRegExp("a?c").test("abbc"), false));
  test("an unclosed class is refused", () => assert.throws(() => shellPatternToRegExp("[ab")));
});

describe("filesListedInFull", () => {
  test("agreeing under the cap", () => assert.equal(filesListedInFull(2, 2), true));
  test("no count", () => assert.equal(filesListedInFull(1, undefined), false));
  test("fewer listed", () => assert.equal(filesListedInFull(1, 2), false));
  test("more listed", () => assert.equal(filesListedInFull(2, 1), false));
  test("at the cap", () => assert.equal(filesListedInFull(300, 300), false));
  test("zero of zero", () => assert.equal(filesListedInFull(0, 0), true));
});

test("pathsOf names a rename's old path", () => assert.deepEqual(pathsOf(changed("a>b", "c")), ["b", "a", "c"]));
test("changesCi finds a definition moved away", () => assert.equal(changesCi(changed(".github/x.yaml>docs/x.yaml")), ".github/x.yaml"));
test("isRenovate needs the app", () => {
  assert.equal(isRenovate({ login: "renovate", bot: true }), true);
  assert.equal(isRenovate({ login: "renovate", bot: false }), false);
});
