// What a step tells the Actions runner: outputs for later steps, and
// annotations in the log.

import { randomBytes } from "node:crypto";
import { appendFileSync } from "node:fs";

import { requireEnv } from "./env.ts";

// OUTPUT_FILE names the file a step appends its outputs to.
const OUTPUT_FILE = "GITHUB_OUTPUT";

// setOutput records name=value for the steps after this one. A value is one
// line: a newline in it would start an output of the value's choosing.
export function setOutput(name: string, value: string): void {
  if (/[\r\n]/.test(value)) {
    throw new Error(`output ${name}: a value spans lines`);
  }
  appendFileSync(requireEnv(OUTPUT_FILE), `${name}=${value}\n`);
}

// escapeData escapes a workflow command's message, as the runner unescapes
// it, so a value cannot end the command and start another.
export function escapeData(message: string): string {
  return message.replaceAll("%", "%25").replaceAll("\r", "%0D").replaceAll("\n", "%0A");
}

// notice, warning and error annotate the run; info is a plain log line.
export function notice(message: string): void {
  console.log(`::notice::${escapeData(message)}`);
}

export function warning(message: string): void {
  console.log(`::warning::${escapeData(message)}`);
}

export function error(message: string): void {
  console.log(`::error::${escapeData(message)}`);
}

export function info(message: string): void {
  console.error(message);
}

// TOKEN_BYTES is how much randomness a stop-commands token carries.
const TOKEN_BYTES = 16;

// withoutWorkflowCommands runs fn with the runner reading no workflow
// command from the log until it returns: what fn prints — commit messages a
// release tool logs, say — can then never act as one. The token that resumes
// them is fresh and printed only to resume.
export async function withoutWorkflowCommands<T>(fn: () => Promise<T>): Promise<T> {
  const token = randomBytes(TOKEN_BYTES).toString("hex");
  console.log(`::stop-commands::${token}`);
  try {
    return await fn();
  } finally {
    console.log(`::${token}::`);
  }
}
