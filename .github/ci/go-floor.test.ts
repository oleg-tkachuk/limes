import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { describe, test } from "node:test";

import { parse } from "yaml";

import { checkFloor, floorOnLine, offFloor } from "./go-floor.ts";

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

test("offFloor names each module off the floor's line", () => {
  assert.deepEqual(offFloor("1.26.9", { ".": "1.26.0", redisreplay: "1.26", otelmetric: "1.25.0" }), ["otelmetric (go 1.25.0)"]);
  assert.deepEqual(offFloor("1.26.9", { ".": "1.26.0" }), []);
});

// The floor job checks the directive of every module it tests, and no other.
test("the floor job checks each module it tests", () => {
  const ci = parse(readFileSync(new URL("../workflows/ci.yaml", import.meta.url), "utf8")) as {
    jobs: { floor: { steps: { run?: string; "working-directory"?: string }[] } };
  };
  const steps = ci.jobs.floor.steps;
  const tested = new Set([".", ...steps.flatMap((s) => (s["working-directory"] === undefined ? [] : [s["working-directory"]]))]);
  const call = steps.find((s) => s.run?.includes("go-floor.ts"))?.run ?? "";
  const checked = new Set(call.split(/\s+/).slice(3));
  assert.deepEqual([...checked].sort(), [...tested].sort());
});

// Every module the floor job checks has a go directive on the floor's line
// today: the tree this test runs in.
test("every tested module's directive is on one line", () => {
  const root = new URL("../../", import.meta.url);
  const directive = (dir: string) => /^go (\S+)$/m.exec(readFileSync(new URL(`${dir}/go.mod`, root), "utf8"))?.[1] ?? "";
  const lines = new Set([".", "redisreplay", "otelmetric", "exchange", "biscuit"].map((d) => directive(d).split(".").slice(0, 2).join(".")));
  assert.equal(lines.size, 1);
});

describe("checkFloor", () => {
  const directives: Record<string, string> = { ".": "1.27.0", exchange: "1.27.0", old: "1.26.0" };
  const read = async (dir: string) => directives[dir] ?? "";
  test("the root's directive by default", async () => assert.deepEqual(await checkFloor(["1.27.3"], read), undefined));
  test("each module named", async () => assert.deepEqual(await checkFloor(["1.27.3", ".", "exchange"], read), undefined));
  test("a module off the floor is named", async () => {
    await assert.rejects(checkFloor(["1.27.3", "exchange", "old"], read), /of old \(go 1\.26\.0\)$/);
  });
  test("refuses no floor", async () => assert.rejects(checkFloor([], read), /usage: go-floor\.ts/));
});
