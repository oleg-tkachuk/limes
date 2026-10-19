// A command whose output goes to the step's log as it runs.

import { spawn } from "node:child_process";

// runInherited runs file with args, its output the step's own, and throws
// when it exits other than 0.
export async function runInherited(file: string, args: readonly string[], options: { cwd?: string; env?: NodeJS.ProcessEnv } = {}): Promise<void> {
  const child = spawn(file, [...args], { cwd: options.cwd, env: options.env, stdio: "inherit" });
  const code = await new Promise<number | null>((resolve, reject) => {
    child.on("error", reject);
    child.on("close", resolve);
  });
  if (code !== 0) {
    throw new Error(`${file} ${args.join(" ")} exited with ${code ?? "a signal"}`);
  }
}
