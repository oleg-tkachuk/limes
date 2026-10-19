// A command whose output goes to the step's log as it runs.

import { spawn } from "node:child_process";

// runInherited runs file with args, its output the step's own, and throws
// when it exits other than 0.
// With timeoutMs, the command is killed once it runs that long, and the call
// throws as for any other failure.
export async function runInherited(
  file: string,
  args: readonly string[],
  options: { cwd?: string; env?: NodeJS.ProcessEnv; timeoutMs?: number } = {},
): Promise<void> {
  const child = spawn(file, [...args], { cwd: options.cwd, env: options.env, stdio: "inherit" });
  let timedOut = false;
  const timer =
    options.timeoutMs === undefined
      ? undefined
      : setTimeout(() => {
          timedOut = true;
          child.kill("SIGKILL");
        }, options.timeoutMs);
  try {
    const code = await new Promise<number | null>((resolve, reject) => {
      child.on("error", reject);
      child.on("close", resolve);
    });
    if (timedOut) {
      throw new Error(`${file} ${args.join(" ")} ran past ${options.timeoutMs} ms and was stopped`);
    }
    if (code !== 0) {
      throw new Error(`${file} ${args.join(" ")} exited with ${code ?? "a signal"}`);
    }
  } finally {
    clearTimeout(timer);
  }
}
