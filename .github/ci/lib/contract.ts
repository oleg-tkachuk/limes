// The changes a release of limes makes to the compatibility policy itself —
// what is contract, how a module is versioned — for its notes: a consumer
// learns of them there rather than by rereading the policy. Defaults and
// codes, which the policy names as contract, have sections of their own.

import { git, lines } from "./git.ts";

// CONTRACT_DOCS are the documents whose every change is one to the contract.
export const CONTRACT_DOCS = ["docs/compatibility.md"];
// CONTRACT_HEADING heads the section of a release's notes naming them.
export const CONTRACT_HEADING = "### Contract";
// SUBJECT_TYPE is a Conventional Commits subject's type, scope and marker,
// which the notes leave out as semantic-release's own sections do.
const SUBJECT_TYPE = /^\w+(?:\([^)]*\))?!?: /;

// contractChanges is the subject of each commit from since to rev that
// changes a CONTRACT_DOCS document, oldest first, its type left out.
export async function contractChanges(root: string, since: string, rev: string): Promise<string[]> {
  const subjects = lines(await git(["log", "--reverse", "--format=%s", `${since}..${rev}`, "--", ...CONTRACT_DOCS], root));
  return subjects.map((s) => s.replace(SUBJECT_TYPE, ""));
}

// withContractChanges is entry with a CONTRACT_HEADING section naming each
// change, added last; entry as it is when there are none.
export function withContractChanges(entry: string, changes: readonly string[]): string {
  if (changes.length === 0) {
    return entry;
  }
  return `${entry.trimEnd()}\n\n${CONTRACT_HEADING}\n\n${changes.map((c) => `* ${c}`).join("\n")}\n`;
}
