import assert from "node:assert/strict";
import { join } from "node:path";
import { describe, test } from "node:test";

import { ROOT_DIR, declaresFuzz, fuzzTargets, parsePackages, targetNames } from "./fuzz-targets.ts";
import { ROOT_MODULE, releasedModules, selectModule } from "./lib/modules.ts";

const ROOT = join(import.meta.dirname, "..", "..");

// The packages and files the fake go and file reader stand in for.
const FUZZED = "example.com/fuzzed";
const UNFUZZED = "example.com/unfuzzed";
const FUZZED_DIR = "/src/fuzzed";
const UNFUZZED_DIR = "/src/unfuzzed";
const FUZZ_FILE = "fuzz_test.go";
const PLAIN_FILE = "plain_test.go";
const FILES: Record<string, string> = {
  [join(FUZZED_DIR, FUZZ_FILE)]: "package fuzzed\n\nfunc FuzzParse(f *testing.F) {}\n",
  [join(UNFUZZED_DIR, PLAIN_FILE)]: "package unfuzzed\n\n// FuzzLike in a comment: func FuzzNot(\nfunc TestPlain(t *testing.T) {}\n",
};
const read = (path: string): string => FILES[path] ?? "";

// LISTED is go list -json's output of the two packages, as go writes it.
const LISTED = `{
	"Dir": "${FUZZED_DIR}",
	"ImportPath": "${FUZZED}",
	"XTestGoFiles": [
		"${FUZZ_FILE}"
	]
}
{
	"Dir": "${UNFUZZED_DIR}",
	"ImportPath": "${UNFUZZED}",
	"TestGoFiles": [
		"${PLAIN_FILE}"
	]
}
`;
const TEST_LIST = `FuzzParse\nFuzzNormalize\nok  \t${FUZZED}\t0.161s\n`;

describe("parsePackages", () => {
  test("reads each package of go list -json", () => {
    assert.deepEqual(
      parsePackages(LISTED).map((p) => p.ImportPath),
      [FUZZED, UNFUZZED],
    );
  });

  test("reads no package from no output", () => {
    assert.deepEqual(parsePackages("\n"), []);
  });
});

describe("declaresFuzz", () => {
  const [fuzzed, unfuzzed] = parsePackages(LISTED);

  test("holds a package whose test file declares a fuzz target", () => {
    assert.ok(fuzzed !== undefined && declaresFuzz(fuzzed, read));
  });

  test("leaves out one that only mentions one", () => {
    assert.ok(unfuzzed !== undefined && !declaresFuzz(unfuzzed, read));
  });
});

test("targetNames leaves out go's ok line", () => {
  assert.deepEqual(targetNames(TEST_LIST), ["FuzzParse", "FuzzNormalize"]);
});

describe("fuzzTargets", () => {
  const modules = releasedModules(ROOT).map((name) => {
    const { dir } = selectModule(ROOT, name);
    return dir === "" ? ROOT_DIR : dir;
  });

  test("lists each module's targets with the module's directory", async () => {
    const calls: { args: readonly string[]; cwd: string }[] = [];
    const targets = await fuzzTargets(
      ROOT,
      async (args, cwd) => {
        calls.push({ args, cwd });
        return args[0] === "list" ? LISTED : TEST_LIST;
      },
      read,
    );
    assert.ok(modules.length > 1, `the table has nested modules: ${modules.join(", ")}`);
    assert.deepEqual(
      calls.filter((c) => c.args[0] === "list").map((c) => c.cwd),
      modules.map((m) => join(ROOT, m)),
    );
    assert.deepEqual(
      targets,
      modules.flatMap((module) => [
        { module, package: FUZZED, target: "FuzzParse" },
        { module, package: FUZZED, target: "FuzzNormalize" },
      ]),
    );
  });

  test("asks go test -list only of a package that declares a target", async () => {
    const listed: string[] = [];
    await fuzzTargets(
      ROOT,
      async (args) => {
        if (args[0] === "test") {
          listed.push(args.at(-1) ?? "");
          return TEST_LIST;
        }
        return LISTED;
      },
      read,
    );
    assert.ok(listed.length > 0 && listed.every((pkg) => pkg === FUZZED));
  });

  test("the root module comes first, in its own directory", () => {
    assert.equal(releasedModules(ROOT)[0], ROOT_MODULE);
    assert.equal(modules[0], ROOT_DIR);
  });
});
