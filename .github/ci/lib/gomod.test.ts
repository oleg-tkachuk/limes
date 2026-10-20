import assert from "node:assert/strict";
import { describe, test } from "node:test";

import { changesBeyondRequirements, statementsBeyondRequirements } from "./gomod.ts";

const BASE = `// A module.
module example.com/nested

go 1.26.0

toolchain go1.27.2

// The replace builds it against the root of this commit.
replace example.com/root => ../

require (
	example.com/root v0.108.3
	github.com/google/uuid v1.6.0
)

require golang.org/x/sys v0.49.0 // indirect
`;

describe("statementsBeyondRequirements", () => {
  test("keeps every directive but require, in order", () =>
    assert.deepEqual(statementsBeyondRequirements(BASE), ["module example.com/nested", "go 1.26.0", "toolchain go1.27.2", "replace example.com/root => ../"]));
  test("reads a block of another directive", () =>
    assert.deepEqual(statementsBeyondRequirements("replace (\n\ta => ../a\n\tb v1.0.0 => c v1.1.0\n)\nexclude (\n  d v1.0.0\n)\n"), [
      "replace a => ../a",
      "replace b v1.0.0 => c v1.1.0",
      "exclude d v1.0.0",
    ]));
});

describe("changesBeyondRequirements", () => {
  const cases: [string, string, boolean][] = [
    ["a requirement raised", BASE.replace("v0.108.3", "v0.109.0"), false],
    ["a requirement added", BASE.replace("require golang.org/x/sys", "require golang.org/x/text v0.43.0 // indirect\nrequire golang.org/x/sys"), false],
    ["a comment and spacing", BASE.replace("// A module.", "// Another comment.").replace("go 1.26.0", "go   1.26.0"), false],
    ["the replace dropped", BASE.replace("replace example.com/root => ../\n", ""), true],
    ["a replace added inside a require-looking line", BASE.replace("require golang.org/x/sys v0.49.0 // indirect", "replace golang.org/x/sys => ../sys"), true],
    ["the toolchain changed", BASE.replace("go1.27.2", "go1.26.0"), true],
    ["the go directive changed", BASE.replace("go 1.26.0", "go 1.25.0"), true],
    ["an ignore added", `${BASE}ignore ./internal\n`, true],
    ["an exclude block added", `${BASE}exclude (\n\texample.com/root v0.108.0\n)\n`, true],
  ];
  for (const [name, after, want] of cases) {
    test(name, () => assert.equal(changesBeyondRequirements(BASE, after), want));
  }
});
