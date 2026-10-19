// Commands run as an argument vector: no shell ever sees an argument.

import { execFile } from "node:child_process";
import { promisify } from "node:util";

const run = promisify(execFile);

// MAX_OUTPUT_BYTES bounds what one command may print: a diff's name list or
// an API listing in a large change is the most any caller reads.
const MAX_OUTPUT_BYTES = 64 * 1024 * 1024;

// ExecOptions is where a command runs and what it is told.
export interface ExecOptions {
  cwd?: string | undefined;
  env?: NodeJS.ProcessEnv | undefined;
}

// execute runs file with args and returns its standard output; it throws,
// with the command's standard error in the message, when it fails.
export async function execute(file: string, args: readonly string[], options: ExecOptions = {}): Promise<string> {
  try {
    const { stdout } = await run(file, [...args], { cwd: options.cwd, env: options.env, maxBuffer: MAX_OUTPUT_BYTES, encoding: "utf8" });
    return stdout;
  } catch (err) {
    const stderr = typeof err === "object" && err !== null && "stderr" in err ? String(err.stderr).trim() : "";
    throw new Error(`${file} ${args.join(" ")} failed${stderr === "" ? "" : `: ${stderr}`}`, { cause: err });
  }
}
