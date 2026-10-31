import assert from "node:assert/strict";
import { test } from "node:test";

import { shadowReport } from "./release-shadow.ts";

test("a plan that matches what the run released has no mismatch", () => {
  const report = shadowReport(["v0.2.0", "pgstore/v0.1.1"], ["v0.2.0"]);
  assert.deepEqual(report.mismatches, []);
  assert.deepEqual(report.rows, [
    ["v0.2.0", "v0.2.0", "same"],
    ["pgstore/v0.1.1", "", "not released this run: a nested module follows limes in a later run"],
  ]);
});

test("a tag the plan and the run disagree on is a mismatch, each way", () => {
  const report = shadowReport(["v0.2.0", "exchange/v0.3.0"], ["v0.3.0", "pgstore/v0.5.0"]);
  assert.deepEqual(report.mismatches, ["limes: planned v0.2.0, released v0.3.0", "pgstore: released pgstore/v0.5.0, not planned"]);
  assert.deepEqual(report.rows, [
    ["v0.2.0", "v0.3.0", "differs"],
    ["exchange/v0.3.0", "", "not released this run: a nested module follows limes in a later run"],
    ["", "pgstore/v0.5.0", "not planned"],
  ]);
});

test("nothing planned and nothing released is no mismatch", () => {
  assert.deepEqual(shadowReport([], []), { rows: [], mismatches: [] });
});
