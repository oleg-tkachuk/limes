import assert from "node:assert/strict";
import { test } from "node:test";

import { floorOnLine } from "./go-floor.ts";

const cases: [string, boolean, string, string][] = [
  ["a patch of a three-part directive", true, "1.26.9", "1.26.0"],
  ["a patch of a two-part directive", true, "1.26.9", "1.26"],
  ["another line, three parts", false, "1.27.1", "1.26.0"],
  ["another line, two parts", false, "1.25.9", "1.26"],
  ["a minor that only shares a prefix", false, "1.2.6", "1.26"],
];
for (const [name, want, floor, directive] of cases) {
  test(name, () => assert.equal(floorOnLine(floor, directive), want));
}
