import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, test } from "node:test";

import { ANNOTATIONS_ENV, ANNOTATIONS_OFF, Level, annotation, cell, code, escapeData, infoLines, setOutput, table } from "./actions.ts";
import { optionalEnv, requireEnv } from "./env.ts";
import { lines } from "./git.ts";

// VARIABLE is the environment variable these tests set and clear.
const VARIABLE = "LIMES_CI_TEST_VARIABLE";

describe("setOutput", () => {
  let dir: string;
  beforeEach(() => {
    dir = mkdtempSync(join(tmpdir(), "limes-ci-"));
    process.env.GITHUB_OUTPUT = join(dir, "output");
  });
  afterEach(() => {
    rmSync(dir, { recursive: true, force: true });
    delete process.env.GITHUB_OUTPUT;
  });

  test("appends name=value", () => {
    setOutput("relevant", "true");
    setOutput("tag", "v1.2.3");
    assert.equal(readFileSync(join(dir, "output"), "utf8"), "relevant=true\ntag=v1.2.3\n");
  });
  test("refuses a value that spans lines", () => {
    assert.throws(() => setOutput("tag", "v1\nrelevant=false"), /spans lines/);
  });
  test("refuses without the output file", () => {
    delete process.env.GITHUB_OUTPUT;
    assert.throws(() => setOutput("tag", "v1"), /GITHUB_OUTPUT is not set/);
  });
});

test("escapeData keeps a message one command", () => {
  assert.equal(escapeData("50%\r\n::error::x"), "50%25%0D%0A::error::x");
});

describe("env", () => {
  afterEach(() => delete process.env[VARIABLE]);
  test("requireEnv names what is missing", () => assert.throws(() => requireEnv(VARIABLE), new RegExp(`${VARIABLE} is not set`)));
  test("an empty value is unset", () => {
    process.env[VARIABLE] = "";
    assert.equal(optionalEnv(VARIABLE), undefined);
    assert.throws(() => requireEnv(VARIABLE));
  });
  test("a value is read", () => {
    process.env[VARIABLE] = "x";
    assert.equal(requireEnv(VARIABLE), "x");
    assert.equal(optionalEnv(VARIABLE), "x");
  });
});

test("lines drops empty lines", () => assert.deepEqual(lines("a\n\nb\n"), ["a", "b"]));

// A log line a change chose — a path named like a command — is no command.
describe("infoLines", () => {
  test("plain text is one line", () => assert.deepEqual(infoLines("a.go\nb.go", "t"), ["a.go\nb.go"]));
  test("anything like a command is fenced off", () =>
    assert.deepEqual(infoLines("a.go\n::warning::x.go", "t0k"), ["::stop-commands::t0k", "a.go\n::warning::x.go", "::t0k::"]));
});

describe("annotation", () => {
  const saved = process.env[ANNOTATIONS_ENV];
  afterEach(() => {
    if (saved === undefined) delete process.env[ANNOTATIONS_ENV];
    else process.env[ANNOTATIONS_ENV] = saved;
  });
  test("a workflow command, escaped", () => {
    delete process.env[ANNOTATIONS_ENV];
    assert.equal(annotation(Level.Error, "a\nb"), "::error::a%0Ab");
  });
  test("a plain line while annotations are off", () => {
    process.env[ANNOTATIONS_ENV] = ANNOTATIONS_OFF;
    assert.equal(annotation(Level.Warning, "x"), "warning: x");
  });
});

// The Taskfile turns annotations off for these tests by the same name and
// value actions.ts reads.
test("verify:ci runs the tests with annotations off", async () => {
  const { readFileSync } = await import("node:fs");
  const { parse } = await import("yaml");
  const taskfile = parse(readFileSync(new URL("../../../Taskfile.yaml", import.meta.url), "utf8")) as {
    tasks: Record<string, { env?: Record<string, string> }>;
  };
  assert.equal(taskfile.tasks["verify:ci"]?.env?.[ANNOTATIONS_ENV], ANNOTATIONS_OFF);
});

test("a code span renders its text literally, whatever backticks it holds", () => {
  assert.equal(code(""), "");
  assert.equal(code("[x](y)"), "`[x](y)`");
  assert.equal(code("a `b` c"), "``a `b` c``");
  assert.equal(code("`edge`"), "`` `edge` ``");
});

test("a table cell holds no line break and no column separator", () => {
  assert.equal(cell("a|b\nc"), "a\\|b c");
  assert.equal(table(["A", "B"], [["1", "x|y"]]), "| A | B |\n| --- | --- |\n| 1 | x\\|y |\n");
});
