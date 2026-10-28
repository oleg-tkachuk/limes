import assert from "node:assert/strict";
import { describe, test } from "node:test";

import { checkNeeds, failedJobs, parseNeeds } from "./checks.ts";

test("passed and skipped jobs pass", () => {
  assert.deepEqual(failedJobs({ verify: { result: "success" }, redis: { result: "skipped" } }), []);
});

test("a failed, cancelled or silent job does not", () => {
  assert.deepEqual(failedJobs({ verify: { result: "failure" }, lint: { result: "cancelled" }, api: {}, ok: { result: "success" } }), [
    "verify (failure)",
    "lint (cancelled)",
    "api (no result)",
  ]);
});

test("the needs context is read as an object of jobs", () => {
  assert.deepEqual(parseNeeds('{"verify":{"result":"success","outputs":{}}}'), { verify: { result: "success", outputs: {} } });
  for (const bad of ["[]", "{}", "null", '"x"']) {
    assert.throws(() => parseNeeds(bad), Error, bad);
  }
});

describe("checkNeeds", () => {
  test("every required job passed or was skipped", () => {
    assert.equal(checkNeeds({ a: { result: "success" }, b: { result: "skipped" } }), "every required job passed or was skipped: a, b");
  });
  test("a failed job is named", () => {
    assert.throws(() => checkNeeds({ a: { result: "success" }, b: { result: "failure" } }), /did not pass: b \(failure\)$/);
  });
});
