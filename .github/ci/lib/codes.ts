// The error codes a release of limes adds, removes or answers otherwise —
// another HTTP status, another category — for its notes and for the check
// that a change to them is marked breaking: a consumer that pins the codes
// on its wire reads the contract change there. docs/errors.md lists every
// code of limes, of its meter and of the exchange with its status and
// category, which the Go tests hold it to, so the contract of two revisions
// is its tables at each.

import { info } from "./actions.ts";
import { git } from "./git.ts";

// ERRORS_DOC is the document that lists every code.
export const ERRORS_DOC = "docs/errors.md";
// FIRST_TABLE opens the tables of the codes: limes's own, the meter's and
// the exchange's, which limes answers alike.
const FIRST_TABLE = "## limes's codes";
// CODE_ROW is a row of those tables: its code, its HTTP status and, in the
// last column, its category.
const CODE_ROW = /^\| `([a-z_]+)` \| ([0-9]{3}) \|.*\| `([a-z_]+)` \|$/gm;
// CODES_HEADING heads the section of a release's notes naming them.
export const CODES_HEADING = "### Codes";

// Answer is what a code is answered with: its HTTP status and category.
export interface Answer {
  status: string;
  category: string;
}

// Changed is a code both revisions have, answered otherwise at the second.
export interface Changed {
  code: string;
  from: Answer;
  to: Answer;
}

// CodeChanges are the codes one revision has and another has not, and those
// both have that it answers otherwise.
export interface CodeChanges {
  added: string[];
  removed: string[];
  changed: Changed[];
}

// NO_CHANGES is a comparison that found nothing.
const NO_CHANGES: CodeChanges = { added: [], removed: [], changed: [] };

// contractIn is each code the tables in doc list, from FIRST_TABLE on,
// with its answer.
export function contractIn(doc: string): Map<string, Answer> {
  const out = new Map<string, Answer>();
  const start = doc.indexOf(FIRST_TABLE);
  if (start < 0) {
    return out;
  }
  for (const [, code, status, category] of doc.slice(start).matchAll(CODE_ROW)) {
    if (code !== undefined && status !== undefined && category !== undefined) {
      out.set(code, { status, category });
    }
  }
  return out;
}

// compare is what is's contract adds to, removes from and changes in was's,
// each sorted by code.
export function compare(was: Map<string, Answer>, is: Map<string, Answer>): CodeChanges {
  const changed: Changed[] = [];
  for (const [code, to] of is) {
    const from = was.get(code);
    if (from !== undefined && (from.status !== to.status || from.category !== to.category)) {
      changed.push({ code, from, to });
    }
  }
  return {
    added: [...is.keys()].filter((c) => !was.has(c)).sort(),
    removed: [...was.keys()].filter((c) => !is.has(c)).sort(),
    changed: changed.sort((a, b) => a.code.localeCompare(b.code)),
  };
}

// breaksWire reports whether changes break a consumer that pins the codes:
// a code removed, or answered with another status or category. A code
// added breaks none.
export function breaksWire(changes: CodeChanges): boolean {
  return changes.removed.length > 0 || changes.changed.length > 0;
}

// describeChange is one changed code as the notes and the check name it.
export function describeChange(c: Changed): string {
  const parts: string[] = [];
  if (c.from.category !== c.to.category) {
    parts.push(`category ${c.from.category} → ${c.to.category}`);
  }
  if (c.from.status !== c.to.status) {
    parts.push(`status ${c.from.status} → ${c.to.status}`);
  }
  return `\`${c.code}\` — ${parts.join(", ")}`;
}

// codeChanges is what rev's contract adds to, removes from and changes in
// since's; none when since has no ERRORS_DOC to compare with.
export async function codeChanges(root: string, since: string, rev: string): Promise<CodeChanges> {
  let before: string;
  try {
    before = await git(["show", `${since}:${ERRORS_DOC}`], root);
  } catch {
    info(`${since} has no ${ERRORS_DOC}: its notes name no code changes`);
    return NO_CHANGES;
  }
  return compare(contractIn(before), contractIn(await git(["show", `${rev}:${ERRORS_DOC}`], root)));
}

// withCodeChanges is entry with a CODES_HEADING section naming changes —
// each code added, removed or answered otherwise —
// added last; entry as it is when there are none.
export function withCodeChanges(entry: string, changes: CodeChanges): string {
  const lines: string[] = [];
  if (changes.added.length > 0) {
    lines.push(`* added: ${changes.added.map((c) => `\`${c}\``).join(", ")}`);
  }
  if (changes.removed.length > 0) {
    lines.push(`* removed: ${changes.removed.map((c) => `\`${c}\``).join(", ")}`);
  }
  for (const c of changes.changed) {
    lines.push(`* changed: ${describeChange(c)}`);
  }
  if (lines.length === 0) {
    return entry;
  }
  return `${entry.trimEnd()}\n\n${CODES_HEADING}\n\n${lines.join("\n")}\n`;
}
