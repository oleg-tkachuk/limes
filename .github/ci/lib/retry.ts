// Calls whose answer may be a moment behind, tried again.
//
// A release dispatched by CI asked the API about the run that dispatched it
// and read a job as in_progress minutes after it had ended; the release
// stopped there. A call that fails every attempt still fails, with its own
// error, and one whose failure is not retryable fails at once.

import { warning } from "./actions.ts";

// RETRY_ATTEMPTS is how many times a call runs before its failure stands;
// RETRY_DELAY_MS, the wait before the second attempt, doubled before each one
// after.
export const RETRY_ATTEMPTS = 4;
export const RETRY_DELAY_MS = 10_000;

export interface RetryOptions {
  attempts?: number;
  delayMs?: number;
  // retryable reports whether a failure may pass on its own; every failure
  // is when it is not given.
  retryable?: (err: unknown) => boolean;
  // sleep waits ms; a test records the waits instead.
  sleep?: (ms: number) => Promise<void>;
}

const pause = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

// retry runs fn until it succeeds, fails in a way that is not retryable, or
// has failed attempts times, and returns its result; what describes the call
// in the log.
export async function retry<T>(what: string, fn: () => Promise<T>, options: RetryOptions = {}): Promise<T> {
  const { attempts, retryable, sleep } = settingsOf(options);
  let delay = options.delayMs ?? RETRY_DELAY_MS;
  for (let attempt = 1; ; attempt++) {
    try {
      return await fn();
    } catch (err) {
      if (attempt >= attempts || !retryable(err)) {
        throw err;
      }
      warning(`${what}: attempt ${attempt} of ${attempts} failed (${err instanceof Error ? err.message : String(err)}); again in ${delay} ms`);
      await sleep(delay);
      delay *= 2;
    }
  }
}

// settingsOf is options with their defaults: RETRY_ATTEMPTS attempts, every
// failure retryable, a real pause between them.
function settingsOf(options: RetryOptions): { attempts: number; retryable: (err: unknown) => boolean; sleep: (ms: number) => Promise<void> } {
  return {
    attempts: options.attempts ?? RETRY_ATTEMPTS,
    retryable: options.retryable ?? (() => true),
    sleep: options.sleep ?? pause,
  };
}
