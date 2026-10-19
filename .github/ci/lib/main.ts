// The entry point every command shares.

import { error } from "./actions.ts";

// runMain runs a command's main and exits non-zero, with the reason as an
// error annotation, when it throws.
export async function runMain(main: () => Promise<void>): Promise<void> {
  try {
    await main();
  } catch (err) {
    error(err instanceof Error ? err.message : String(err));
    process.exitCode = 1;
  }
}
