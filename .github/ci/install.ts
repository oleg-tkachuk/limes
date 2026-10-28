// Install a package's locked dependencies, with no install script, bounded
// in time and tried again.
//
// Usage: node install.ts <dir>...
//
// npm ci once hung on the registry for the whole of a job's time limit. Each
// attempt here is stopped after ATTEMPT_TIMEOUT_MS and tried again, up to
// ATTEMPTS, a pause between; npm's own fetches time out and retry sooner
// still. Only npm ci --ignore-scripts runs: no package's install script, in
// jobs that may hold a write token. It needs no dependency of its own, so it
// runs before any is installed.

import { info, warning } from "./lib/actions.ts";
import { runMain } from "./lib/main.ts";
import { runInherited } from "./lib/spawn.ts";

// ATTEMPTS, ATTEMPT_TIMEOUT_MS and PAUSE_MS bound one install.
export const ATTEMPTS = 3;
export const ATTEMPT_TIMEOUT_MS = 3 * 60 * 1000;
const PAUSE_MS = 10_000;
// NPM_FETCH is npm's own bound on one request, and how often it retries one.
const NPM_FETCH = { NPM_CONFIG_FETCH_TIMEOUT: "60000", NPM_CONFIG_FETCH_RETRIES: "3" };

// Install runs one attempt of npm ci in dir.
export type Install = (dir: string) => Promise<void>;

// npmCi is the real attempt.
export const npmCi: Install = (dir) =>
  runInherited("npm", ["ci", "--ignore-scripts"], { cwd: dir, env: { ...process.env, ...NPM_FETCH }, timeoutMs: ATTEMPT_TIMEOUT_MS });

// installWithRetries installs dir, trying again after a failed or stopped
// attempt, and throws the last failure once every attempt failed.
export async function installWithRetries(dir: string, install: Install = npmCi, pauseMs: number = PAUSE_MS): Promise<void> {
  for (let attempt = 1; ; attempt++) {
    try {
      await install(dir);
      info(`installed ${dir}`);
      return;
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      if (attempt >= ATTEMPTS) {
        throw new Error(`could not install ${dir} in ${ATTEMPTS} attempts: ${message}`);
      }
      warning(`installing ${dir} failed (attempt ${attempt} of ${ATTEMPTS}): ${message}`);
      await new Promise((resolve) => setTimeout(resolve, pauseMs));
    }
  }
}

async function main(): Promise<void> {
  await installAll(process.argv.slice(2), installWithRetries);
}

// installAll installs each of dirs with install, refusing none.
export async function installAll(dirs: readonly string[], install: (dir: string) => Promise<void>): Promise<void> {
  if (dirs.length === 0) {
    throw new Error("usage: install.ts <dir>...");
  }
  for (const dir of dirs) {
    await install(dir);
  }
}

if (import.meta.main) {
  await runMain(main);
}
