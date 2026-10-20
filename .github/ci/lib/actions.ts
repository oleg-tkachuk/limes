// What a step tells the Actions runner: outputs for later steps, and
// annotations in the log.

import { randomBytes } from "node:crypto";
import { appendFileSync } from "node:fs";

import { requireEnv } from "./env.ts";

// OUTPUT_FILE names the file a step appends its outputs to; SUMMARY_FILE,
// the one it appends the Markdown of the run's summary page to.
const OUTPUT_FILE = "GITHUB_OUTPUT";
const SUMMARY_FILE = "GITHUB_STEP_SUMMARY";

// setOutput records name=value for the steps after this one. A value is one
// line: a newline in it would start an output of the value's choosing.
export function setOutput(name: string, value: string): void {
  if (/[\r\n]/.test(value)) {
    throw new Error(`output ${name}: a value spans lines`);
  }
  appendFileSync(requireEnv(OUTPUT_FILE), `${name}=${value}\n`);
}

// summarize appends markdown to the run's summary page; outside a run, with
// no page to write to, it does nothing.
export function summarize(markdown: string): void {
  const file = process.env[SUMMARY_FILE];
  if (file !== undefined && file !== "") {
    appendFileSync(file, markdown.endsWith("\n") ? markdown : `${markdown}\n`);
  }
}

// cell is text as one cell of a Markdown table: no line break, no column
// separator of its own.
export function cell(text: string): string {
  return text.replaceAll("\r", " ").replaceAll("\n", " ").replaceAll("|", "\\|");
}

// table is a Markdown table of rows under header.
export function table(header: readonly string[], rows: readonly (readonly string[])[]): string {
  const line = (cells: readonly string[]) => `| ${cells.map(cell).join(" | ")} |`;
  return [line(header), line(header.map(() => "---")), ...rows.map(line)].join("\n") + "\n";
}

// escapeData escapes a workflow command's message, as the runner unescapes
// it, so a value cannot end the command and start another.
export function escapeData(message: string): string {
  return message.replaceAll("%", "%25").replaceAll("\r", "%0D").replaceAll("\n", "%0A");
}

// ANNOTATIONS_ENV, set to ANNOTATIONS_OFF, writes an annotation as a plain
// log line: the tests that drive a command through its refusals set it, so
// a refusal a test expects is not shown on the run as one of the run's own.
export const ANNOTATIONS_ENV = "LIMES_CI_ANNOTATIONS";
export const ANNOTATIONS_OFF = "off";

// Level is the kind of an annotation.
export const Level = { Notice: "notice", Warning: "warning", Error: "error" } as const;
export type Level = (typeof Level)[keyof typeof Level];

// annotation is the line that annotates the run with message at level, or
// the plain line that stands for it while annotations are off.
export function annotation(level: Level, message: string): string {
  return process.env[ANNOTATIONS_ENV] === ANNOTATIONS_OFF ? `${level}: ${message}` : `::${level}::${escapeData(message)}`;
}

// notice, warning and error annotate the run; info is a plain log line.
export function notice(message: string): void {
  console.log(annotation(Level.Notice, message));
}

export function warning(message: string): void {
  console.log(annotation(Level.Warning, message));
}

export function error(message: string): void {
  console.log(annotation(Level.Error, message));
}

// TOKEN_BYTES is how much randomness a stop-commands token carries.
const TOKEN_BYTES = 16;

// COMMAND_PREFIX starts a line the runner reads as a workflow command.
const COMMAND_PREFIX = "::";

// freshToken is a stop-commands token no log line can have guessed.
function freshToken(): string {
  return randomBytes(TOKEN_BYTES).toString("hex");
}

// infoLines is message as the lines that log it: inside a stop-commands
// block under token when it holds anything the runner could read as a
// workflow command — a path, a branch name or a title a change chose.
export function infoLines(message: string, token: string): string[] {
  if (!message.includes(COMMAND_PREFIX)) {
    return [message];
  }
  return [`${COMMAND_PREFIX}stop-commands${COMMAND_PREFIX}${token}`, message, `${COMMAND_PREFIX}${token}${COMMAND_PREFIX}`];
}

// info is a plain log line, never a workflow command. The block's markers go
// to the stream the message does: the runner reads two streams in no order
// between them.
export function info(message: string): void {
  for (const line of infoLines(message, freshToken())) {
    console.error(line);
  }
}

// withoutWorkflowCommands runs fn with the runner reading no workflow
// command from the log until it returns: what fn prints — commit messages a
// release tool logs, say — can then never act as one. The token that resumes
// them is fresh and printed only to resume.
export async function withoutWorkflowCommands<T>(fn: () => Promise<T>): Promise<T> {
  const token = freshToken();
  console.log(`::stop-commands::${token}`);
  try {
    return await fn();
  } finally {
    console.log(`::${token}::`);
  }
}
