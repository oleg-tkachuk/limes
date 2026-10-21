import assert from "node:assert/strict";
import { describe, test } from "node:test";

import { retry } from "./retry.ts";

// recorder is a sleep that records each wait instead of waiting.
function recorder(): { waits: number[]; sleep: (ms: number) => Promise<void> } {
  const waits: number[] = [];
  return { waits, sleep: async (ms) => void waits.push(ms) };
}

// failing is a call that fails its first n attempts with err, then answers
// "ok"; calls counts the attempts.
function failing(n: number, err: Error = new Error("not yet")): { calls: () => number; fn: () => Promise<string> } {
  let calls = 0;
  return {
    calls: () => calls,
    fn: async () => {
      calls++;
      if (calls <= n) {
        throw err;
      }
      return "ok";
    },
  };
}

describe("retry", () => {
  test("a call that succeeds at once is not waited on", async () => {
    const { waits, sleep } = recorder();
    const call = failing(0);
    assert.equal(await retry("call", call.fn, { sleep }), "ok");
    assert.equal(call.calls(), 1);
    assert.deepEqual(waits, []);
  });
  test("each wait doubles the one before", async () => {
    const { waits, sleep } = recorder();
    const call = failing(3);
    assert.equal(await retry("call", call.fn, { attempts: 4, delayMs: 10, sleep }), "ok");
    assert.equal(call.calls(), 4);
    assert.deepEqual(waits, [10, 20, 40]);
  });
  test("the last failure stands once every attempt failed", async () => {
    const { waits, sleep } = recorder();
    const call = failing(5, new Error("still down"));
    await assert.rejects(retry("call", call.fn, { attempts: 3, delayMs: 1, sleep }), /still down/);
    assert.equal(call.calls(), 3);
    assert.deepEqual(waits, [1, 2]);
  });
  test("a failure that is not retryable fails at once", async () => {
    const { waits, sleep } = recorder();
    const call = failing(5, new Error("wrong commit"));
    await assert.rejects(retry("call", call.fn, { sleep, retryable: () => false }), /wrong commit/);
    assert.equal(call.calls(), 1);
    assert.deepEqual(waits, []);
  });
});
