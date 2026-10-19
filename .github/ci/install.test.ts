import assert from "node:assert/strict";
import { test } from "node:test";

import { ATTEMPTS, installWithRetries } from "./install.ts";
import { runInherited } from "./lib/spawn.ts";

test("a failed attempt is tried again", async () => {
  let calls = 0;
  await installWithRetries("dir", async () => {
    if (++calls < 2) throw new Error("hung");
  }, 0);
  assert.equal(calls, 2);
});

test("every attempt failing is an error", async () => {
  let calls = 0;
  await assert.rejects(
    installWithRetries("dir", async () => {
      calls++;
      throw new Error("registry down");
    }, 0),
    /in 3 attempts: registry down/,
  );
  assert.equal(calls, ATTEMPTS);
});

test("a command that runs too long is stopped", async () => {
  await assert.rejects(runInherited(process.execPath, ["-e", "setTimeout(() => {}, 60000)"], { timeoutMs: 200 }), /was stopped/);
});

test("a command that exits non-zero fails", async () => {
  await assert.rejects(runInherited(process.execPath, ["-e", "process.exit(3)"]), /exited with 3/);
});
