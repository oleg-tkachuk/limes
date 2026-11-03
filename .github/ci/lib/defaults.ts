// What a module's Config fields do at their zero value, as a release of it
// changes them — for its notes and for the check that such a change is
// marked breaking (docs/compatibility.md). docs/defaults.md lists every
// field under its module's section, which a Go test holds it to, so a
// module's defaults at two revisions are its section's rows at each.

import { info } from "./actions.ts";
import { git } from "./git.ts";

// DEFAULTS_DOC is the document that lists every Config field.
export const DEFAULTS_DOC = "docs/defaults.md";
// SECTION opens a module's table, named as the module is.
const SECTION = /^## (\S+)$/;
// ROW is a row of it: the package's Config, its field, and what zero does.
const ROW = /^\| `([a-z]+\.[A-Z]\w*)` \| `([A-Z]\w*)` \| (.+) \|$/;
// DEFAULTS_HEADING heads the section of a release's notes naming them.
export const DEFAULTS_HEADING = "### Defaults";

// DefaultChange is a field both revisions list, doing otherwise at zero.
export interface DefaultChange {
  field: string;
  from: string;
  to: string;
}

// DefaultChanges are a module's fields one revision lists and another does
// not, and those both list whose zero does otherwise.
export interface DefaultChanges {
  added: string[];
  removed: string[];
  changed: DefaultChange[];
}

const NO_CHANGES: DefaultChanges = { added: [], removed: [], changed: [] };

// defaultsIn is each field module's section of doc lists, as Config.Field,
// with what its zero does.
export function defaultsIn(doc: string, module: string): Map<string, string> {
  const out = new Map<string, string>();
  let section = "";
  for (const line of doc.split("\n")) {
    const heading = SECTION.exec(line);
    if (heading !== null) {
      section = heading[1] ?? "";
      continue;
    }
    const row = ROW.exec(line);
    if (section === module && row !== null) {
      out.set(`${row[1]}.${row[2]}`, row[3] ?? "");
    }
  }
  return out;
}

// compareDefaults is what is adds to, removes from and changes in was, each
// sorted by field.
export function compareDefaults(was: Map<string, string>, is: Map<string, string>): DefaultChanges {
  const changed: DefaultChange[] = [];
  for (const [field, to] of is) {
    const from = was.get(field);
    if (from !== undefined && from !== to) {
      changed.push({ field, from, to });
    }
  }
  return {
    added: [...is.keys()].filter((f) => !was.has(f)).sort(),
    removed: [...was.keys()].filter((f) => !is.has(f)).sort(),
    changed: changed.sort((a, b) => a.field.localeCompare(b.field)),
  };
}

// breaksDefaults reports whether changes break a consumer leaving the
// fields zero: a field removed, or doing otherwise. A field added, whose zero
// keeps the old behaviour, breaks none.
export function breaksDefaults(changes: DefaultChanges): boolean {
  return changes.removed.length > 0 || changes.changed.length > 0;
}

// describeDefaultChange is one changed field as the notes and the check name it.
export function describeDefaultChange(c: DefaultChange): string {
  return `\`${c.field}\` — ${c.from} → ${c.to}`;
}

// defaultChanges is what rev adds to, removes from and changes in since's
// defaults of module; none when since has no DEFAULTS_DOC to compare with.
export async function defaultChanges(root: string, module: string, since: string, rev: string): Promise<DefaultChanges> {
  let before: string;
  try {
    before = await git(["show", `${since}:${DEFAULTS_DOC}`], root);
  } catch {
    info(`${since} has no ${DEFAULTS_DOC}: no default changes of ${module} to name`);
    return NO_CHANGES;
  }
  return compareDefaults(defaultsIn(before, module), defaultsIn(await git(["show", `${rev}:${DEFAULTS_DOC}`], root), module));
}

// withDefaultChanges is entry with a DEFAULTS_HEADING section naming each
// field whose zero does otherwise, or that is gone, added last; entry as it
// is when there are none. A field added keeps the old behaviour, which its
// Go API change already shows.
export function withDefaultChanges(entry: string, changes: DefaultChanges): string {
  const lines = [...changes.removed.map((f) => `* removed: \`${f}\``), ...changes.changed.map((c) => `* changed: ${describeDefaultChange(c)}`)];
  if (lines.length === 0) {
    return entry;
  }
  return `${entry.trimEnd()}\n\n${DEFAULTS_HEADING}\n\n${lines.join("\n")}\n`;
}
