// How a commit marks itself breaking, and which of a module's commits count.

import { git, lines } from "./git.ts";
import type { Module, ReleaseCommits } from "./modules.ts";

// SUBJECT_MARK and FOOTER_MARK are the subject marker and the footer
// Conventional Commits 1.0 define, as the release's commit parser reads them:
// either case, any word as the type, and the footer after leading spaces, a |
// or a *, followed by a colon or a space, on any line but the subject. The
// type is ASCII, as the parser's \w is; a scope runs to the last ) before the
// !, as its (.*) does. Anything it reads as breaking, these must too.
const SUBJECT_MARK = /^[A-Za-z0-9_]*(\(.*\))?!: /i;
const FOOTER_MARK = /^[\s|*]*BREAKING[ -]CHANGE[:\s]/i;

// messageMarksBreaking reports whether a commit message marks itself
// breaking.
export function messageMarksBreaking(message: string): boolean {
  const [subject = "", ...body] = message.split("\n");
  return SUBJECT_MARK.test(subject) || body.some((line) => FOOTER_MARK.test(line));
}

// titleMarksBreaking reports whether a pull request title would mark its
// merge commit breaking: the title becomes the commit's body, where the
// release's parser reads a BREAKING CHANGE footer.
export function titleMarksBreaking(title: string): boolean {
  return FOOTER_MARK.test(title);
}

// messageOf is a commit's full message.
async function messageOf(hash: string, cwd?: string): Promise<string> {
  return git(["log", "-1", "--format=%B", hash], cwd);
}

// moduleCommits is the commits in from..to that touch the module and that
// its own release rules cut a release for: a marker scoped to another module,
// which those rules release nothing of, never reaches it.
export async function moduleCommits(module: Module, from: string, to: string, commits: ReleaseCommits, cwd: string): Promise<string[]> {
  const hashes = lines(await git(["log", "--format=%H", `${from}..${to}`], cwd));
  if (module.table === undefined) {
    return hashes;
  }
  const { scope, analyzer } = module.table.configFor(module.name);
  const counted: string[] = [];
  for (const hash of hashes) {
    if (commits.touches(commits.changedPaths(cwd, hash), scope) && (await commits.releases(analyzer, hash, await messageOf(hash, cwd)))) {
      counted.push(hash);
    }
  }
  return counted;
}

// marksBreaking reports whether a commit in from..to that touches the module
// marks itself breaking, and is one its release counts. A failure reading the
// commits — the release analyzer not loading, say — is thrown, never read as
// a range with no marker in it.
export async function marksBreaking(module: Module, from: string, to: string, commits: ReleaseCommits, cwd: string): Promise<boolean> {
  for (const hash of await moduleCommits(module, from, to, commits, cwd)) {
    if (messageMarksBreaking(await messageOf(hash, cwd))) {
      return true;
    }
  }
  return false;
}
