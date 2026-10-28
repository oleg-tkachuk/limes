import assert from "node:assert/strict";
import { afterEach, describe, test } from "node:test";

import { withoutWorkflowCommands } from "./actions.ts";
import { runMain } from "./main.ts";

describe("runMain", () => {
  afterEach(() => {
    process.exitCode = undefined;
  });
  for (const [name, main, want] of [
    ["a main that returns nothing", async () => {}, undefined],
    ["a main that reports it passed", async () => true, undefined],
    ["a main that reports it failed", async () => false, 1],
    ["a main that throws", async () => { throw new Error("refused"); }, 1],
  ] as const) {
    test(`${name} exits ${want ?? "zero"}`, async () => {
      await withoutWorkflowCommands(() => runMain(main));
      assert.equal(process.exitCode, want);
    });
  }
});
