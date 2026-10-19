import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, test } from "node:test";

import { highestLevel, nextVersion, parseVersion } from "./prerelease.ts";
import { Level, POLICY_FILE, isDue, isStableTag, loadPolicy, typeOf, type Pending, type Policy } from "./release-policy.ts";

const POLICY: Policy = { minorCommits: 3, patchCommits: 5, immediateTypes: ["security", "build"] };
const REPO_ROOT = new URL("../../../", import.meta.url).pathname;

function pending(subject: string, level: Level, breaking = false): Pending {
  return { hash: "h", subject, type: typeOf(subject), breaking, level };
}

describe("isDue", () => {
  const cases: [string, Pending[], boolean][] = [
    ["nothing pending", [], false],
    ["two minors", [pending("feat: a", Level.Minor), pending("feat: b", Level.Minor)], false],
    ["three minors", Array.from({ length: 3 }, (_, i) => pending(`feat: ${i}`, Level.Minor)), true],
    ["four patches", Array.from({ length: 4 }, (_, i) => pending(`fix: ${i}`, Level.Patch)), false],
    ["five patches", Array.from({ length: 5 }, (_, i) => pending(`fix: ${i}`, Level.Patch)), true],
    ["a security fix", [pending("security: seal", Level.Patch)], true],
    ["a dependency bump", [pending("build(pgstore): require limes v0.2.0", Level.Patch)], true],
    ["a breaking change", [pending("feat!: change", Level.Minor, true)], true],
    ["a major", [pending("feat: change", Level.Major)], true],
    ["two minors and four patches", [pending("feat: a", Level.Minor), pending("feat: b", Level.Minor), ...Array.from({ length: 4 }, (_, i) => pending(`fix: ${i}`, Level.Patch))], false],
  ];
  for (const [name, commits, want] of cases) {
    test(name, () => assert.equal(isDue(commits, POLICY).due, want));
  }
});

test("typeOf", () => {
  assert.equal(typeOf("feat(api)!: x"), "feat");
  assert.equal(typeOf("Security: x"), "security");
  assert.equal(typeOf("no type here"), "");
});

test("isStableTag", () => {
  assert.equal(isStableTag("v1.2.3", ""), true);
  assert.equal(isStableTag("v1.2.3-rc.1", ""), false);
  assert.equal(isStableTag("pgstore/v0.1.0", "pgstore/"), true);
});

describe("loadPolicy", () => {
  test("the repository's own", () => {
    const policy = loadPolicy(REPO_ROOT);
    assert.ok(policy.minorCommits > 0 && policy.patchCommits > 0);
    assert.ok(policy.immediateTypes.includes("security"));
  });
  for (const [name, body] of [
    ["a zero threshold", { minorCommits: 0, patchCommits: 5, immediateTypes: [] }],
    ["a fraction", { minorCommits: 1.5, patchCommits: 5, immediateTypes: [] }],
    ["no immediate types", { minorCommits: 3, patchCommits: 5 }],
  ] as const) {
    test(`refuses ${name}`, () => {
      const dir = mkdtempSync(join(tmpdir(), "limes-policy-"));
      try {
        mkdirSync(join(dir, ".github/release"), { recursive: true });
        writeFileSync(join(dir, POLICY_FILE), JSON.stringify(body));
        assert.throws(() => loadPolicy(dir));
      } finally {
        rmSync(dir, { recursive: true, force: true });
      }
    });
  }
});

describe("versions", () => {
  test("highestLevel", () => {
    assert.equal(highestLevel([pending("fix: a", Level.Patch), pending("feat: b", Level.Minor)]), Level.Minor);
    assert.equal(highestLevel([]), undefined);
  });
  test("nextVersion", () => {
    const last = parseVersion("pgstore/v0.29.2", "pgstore/");
    assert.equal(nextVersion(last, Level.Patch), "0.29.3");
    assert.equal(nextVersion(last, Level.Minor), "0.30.0");
    assert.equal(nextVersion(last, Level.Major), "1.0.0");
    assert.throws(() => nextVersion(parseVersion("v1.4.0", ""), Level.Major), /module path/);
    assert.throws(() => parseVersion("v1.4", ""));
  });
});
