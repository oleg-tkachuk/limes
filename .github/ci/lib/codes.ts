// The error codes a release of limes adds or removes, for its notes: a
// consumer that pins the codes on its wire reads the contract change there.
// docs/errors.md lists every code of limes and of its meter, which the Go
// tests hold it to, so the codes of two revisions are those of its tables
// at each.

import { info } from "./actions.ts";
import { git } from "./git.ts";

// ERRORS_DOC is the document that lists every code.
export const ERRORS_DOC = "docs/errors.md";
// FIRST_TABLE opens the tables of limes's own codes and the meter's;
// AFTER_TABLES, the exchange's, which its own module releases.
const FIRST_TABLE = "## limes's codes";
const AFTER_TABLES = "## The exchange's codes";
// CODE_ROW is a row of those tables: its code, then its HTTP status.
const CODE_ROW = /^\| `([a-z_]+)` \| [0-9]{3} \|/gm;
// CODES_HEADING heads the section of a release's notes naming them.
export const CODES_HEADING = "### Codes";

// CodeChanges are the codes one revision has and another has not.
export interface CodeChanges {
  added: string[];
  removed: string[];
}

// codesIn is the set of codes the tables of limes and its meter in doc list.
export function codesIn(doc: string): Set<string> {
  const start = doc.indexOf(FIRST_TABLE);
  if (start < 0) {
    return new Set();
  }
  const end = doc.indexOf(AFTER_TABLES, start);
  const tables = doc.slice(start, end < 0 ? undefined : end);
  return new Set([...tables.matchAll(CODE_ROW)].flatMap((m) => (m[1] === undefined ? [] : [m[1]])));
}

// codeChanges is what rev's codes add to and remove from since's, sorted;
// none when since has no ERRORS_DOC to compare with.
export async function codeChanges(root: string, since: string, rev: string): Promise<CodeChanges> {
  let before: string;
  try {
    before = await git(["show", `${since}:${ERRORS_DOC}`], root);
  } catch {
    info(`${since} has no ${ERRORS_DOC}: its notes name no code changes`);
    return { added: [], removed: [] };
  }
  const was = codesIn(before);
  const is = codesIn(await git(["show", `${rev}:${ERRORS_DOC}`], root));
  return {
    added: [...is].filter((c) => !was.has(c)).sort(),
    removed: [...was].filter((c) => !is.has(c)).sort(),
  };
}

// withCodeChanges is entry with a CODES_HEADING section naming changes,
// added last; entry as it is when there are none.
export function withCodeChanges(entry: string, changes: CodeChanges): string {
  const lines: string[] = [];
  if (changes.added.length > 0) {
    lines.push(`* added: ${changes.added.map((c) => `\`${c}\``).join(", ")}`);
  }
  if (changes.removed.length > 0) {
    lines.push(`* removed: ${changes.removed.map((c) => `\`${c}\``).join(", ")}`);
  }
  if (lines.length === 0) {
    return entry;
  }
  return `${entry.trimEnd()}\n\n${CODES_HEADING}\n\n${lines.join("\n")}\n`;
}
