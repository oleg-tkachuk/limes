// Who the merge automation acts for, on which branch, and which changes it
// leaves to a person.
//
// merge-green.ts merges a pull request through AUTOMERGE_TOKEN only for one
// into BASE_BRANCH whose author could have pushed the change themselves, so
// the token never does for someone what they could not do.

import type { Author, ChangedFile, GitHub } from "./lib/github.ts";
import { shellPatternToRegExp } from "./lib/glob.ts";
import { changesBeyondRequirements } from "./lib/gomod.ts";

// BASE_BRANCH is the branch the automation merges into.
export const BASE_BRANCH = "main";

// PUSH_PERMISSIONS are the repository permissions that can push a branch, as
// the collaborator permission API names them (maintain reads as write there).
const PUSH_PERMISSIONS: readonly string[] = ["admin", "write"];

// RENOVATE is Renovate as the author of a pull request. Renovate merges its
// own, once a release is old enough (renovate.json).
const RENOVATE: Author = { login: "renovate", bot: true };

// CI_DEFINITION are the paths that decide what CI runs, as bash patterns,
// whose `*` crosses `/`: the workflows and their scripts, the tasks CI calls,
// the linter's configuration, the pinned tools, and the root go.mod, whose go
// and toolchain lines pick the Go every job sets up; a workspace at any
// depth, which overrides every module's requirements in each job that runs go
// from there down; a vendor directory at any depth, which replaces the source
// a module builds; Task's own settings and its cache of remote includes and
// checksums (.task); and Renovate's, which decide what Renovate merges into
// main with no person reading it. A pull request runs the workflows and tasks
// of its own head, so one that changes them can make "All checks passed"
// pass without the checks behind it: its green check is no evidence, and it
// is left for a person. Task and golangci-lint each read whichever of several
// file names exists, and Task prefers Taskfile.yml to Taskfile.yaml, so a new
// file under another of those names replaces the reviewed one: every spelling
// counts. A nested module's go.mod is not one of them: the release raises its
// requirement of limes in a pull request merged this way. Its other lines —
// replace, ignore, go, toolchain — decide what CI tests as surely, so a change
// to them is held all the same (nestedModuleChange).
export const CI_DEFINITION: readonly string[] = [
  ".github/*",
  "[Tt]askfile.*",
  ".taskrc*",
  ".task/*",
  ".golangci.*",
  "tools/*",
  "go.mod",
  "go.work*",
  "*/go.work*",
  "vendor/*",
  "*/vendor/*",
  "renovate.json*",
  ".renovaterc*",
];

const CI_DEFINITION_RE = CI_DEFINITION.map(shellPatternToRegExp);

// COMPARE_FILES_CAP is the most files the compare API lists for two commits,
// which lists a commit's own files rather than those of a head that may move:
// past it the rest are never seen, so a pull request that reaches it is left
// for a person.
export const COMPARE_FILES_CAP = 300;

// isRenovate reports whether author is Renovate.
export function isRenovate(author: Author): boolean {
  return author.bot === RENOVATE.bot && author.login === RENOVATE.login;
}

// definesCi reports whether path is one CI_DEFINITION names.
export function definesCi(path: string): boolean {
  return CI_DEFINITION_RE.some((re) => re.test(path));
}

// pathsOf is every path a listing names: a renamed file's old path too, as
// moving a CI definition out of its place changes what CI runs as surely as
// deleting it does.
export function pathsOf(files: readonly ChangedFile[]): string[] {
  return files.flatMap((f) => (f.previousFilename === undefined ? [f.filename] : [f.filename, f.previousFilename]));
}

// filesListedInFull reports whether listed files of a pull request GitHub
// says changes changed are all of them: changed is a count, the two agree,
// and the listing did not stop at the cap of the API that listed them. A
// count GitHub did not give is never read as a complete listing, and neither
// is one the listing disagrees with either way: more files than changed means
// the two answers describe different heads.
export function filesListedInFull(listed: number, changed: number | undefined, cap: number = COMPARE_FILES_CAP): boolean {
  return changed !== undefined && Number.isInteger(changed) && listed === changed && listed < cap;
}

// changesCi is the first path of files that defines what CI runs, or
// undefined when none does.
export function changesCi(files: readonly ChangedFile[]): string | undefined {
  return pathsOf(files).find(definesCi);
}

// GO_MOD names a module's definition.
const GO_MOD = "go.mod";

// isNestedGoMod reports whether path is a go.mod below the root: the root's
// is a CI definition outright.
export function isNestedGoMod(path: string): boolean {
  return path.endsWith(`/${GO_MOD}`);
}

// nestedModuleChange is the first nested go.mod of files that changes more
// than its requirements between base and head — or that one of them does not
// hold — or undefined when none does.
export async function nestedModuleChange(github: GitHub, files: readonly ChangedFile[], base: string, head: string): Promise<string | undefined> {
  for (const path of pathsOf(files).filter(isNestedGoMod)) {
    const [before, after] = await Promise.all([github.fileAt(path, base), github.fileAt(path, head)]);
    if (before === undefined || after === undefined || changesBeyondRequirements(before, after)) {
      return path;
    }
  }
  return undefined;
}

// canPush reports whether login can push to the repository. An author the API
// does not know as a collaborator — an app, a user with no access — cannot,
// and nor can one whose permission cannot be read.
export async function canPush(github: GitHub, login: string): Promise<boolean> {
  try {
    const permission = await github.permission(login);
    return permission !== undefined && PUSH_PERMISSIONS.includes(permission);
  } catch {
    return false;
  }
}
