import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, test } from "node:test";

import { escapeData, setOutput } from "./actions.ts";
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
