// The entry point every command shares.

import { error } from "./actions.ts";

// FAILED is the exit code of a command that did not do what it was asked.
const FAILED = 1;

// runMain runs a command's main and exits non-zero when it throws, with the
// reason as an error annotation, or when it reports, by returning false,
// that it failed without throwing: having reported each failure itself.
export async function runMain(main: () => Promise<void | boolean>): Promise<void> {
  try {
    if ((await main()) === false) {
      process.exitCode = FAILED;
    }
  } catch (err) {
    error(err instanceof Error ? err.message : String(err));
    process.exitCode = FAILED;
  }
}
