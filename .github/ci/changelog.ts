// Each module's CHANGELOG.md, written from its releases.
//
// Usage: node changelog.ts check
//        GH_TOKEN=… GITHUB_REPOSITORY=… node changelog.ts write | propose
//
// A module's changelog is in its directory, beside its go.mod, so the Go
// proxy's copy of a release carries it. It holds one entry per release
// tag of the module that names no prerelease, newest first: the notes
// semantic-release generated from the commits, as the GitHub release
// publishes them, without GitHub's list of merged pull requests below them —
// that list spans the whole repository, not the module. A release published
// with no such notes gets its heading and a link to the release.
//
// No entry is written by hand. check, run by CI, holds every file to the
// tags with no network: each entry names a release tag reachable from HEAD,
// newest first, once. An entry missing is not refused — a pull request cut
// after a release does not have it until the proposal below merges. write
// adds every missing entry, reading the release's notes from GitHub, and
// leaves the entries already written as they are. propose, run after a
// release, commits what write adds on BRANCH cut from main and opens or
// updates its pull request, which auto-merge.yaml merges once green; a docs
// commit releases nothing, so the merge cuts no release of its own. Without
// GH_TOKEN it warns and does nothing, as require-limes.ts does.

import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import { info, warning } from "./lib/actions.ts";
import { optionalEnv, requireEnv } from "./lib/env.ts";
import { git } from "./lib/git.ts";
import { type GitHub, gitHubFor } from "./lib/github.ts";
import { runMain } from "./lib/main.ts";
import { type Module, releasedModules, selectModule } from "./lib/modules.ts";
import { releaseTagsBelow } from "./lib/releases.ts";
import { BASE_BRANCH, pushAs } from "./require-limes.ts";

// CHANGELOG is the file's name in every module's directory.
export const CHANGELOG = "CHANGELOG.md";
// TITLE opens every changelog.
export const TITLE = "# Changelog";
// GENERATED_MARKER opens the part of a release's body GitHub generates
// (.github/release.yml): the list of merged pull requests, left out.
export const GENERATED_MARKER = "<!-- Release notes generated using configuration in .github/release.yml";
// ENTRY_HEADING opens an entry and names its version: semantic-release's
// heading, which links the version to a comparison with the release before.
const ENTRY_HEADING = /^## \[([0-9]+\.[0-9]+\.[0-9]+)\]/;
// TOP_HEADING is a heading semantic-release may open the notes with at the
// top level, which the changelog's TITLE holds: it becomes an entry's.
const TOP_HEADING = /^# \[/;
// BRANCH is the branch propose commits on.
export const BRANCH = "docs/changelog";
// TITLE_TAGS is how many tags a proposal's title names; more are counted.
const TITLE_TAGS = 3;
const REMOTE = "origin";

// changelogPath is module's changelog, from the checkout's root.
export function changelogPath(module: Module): string {
  return module.dir === "" ? CHANGELOG : join(module.dir, CHANGELOG);
}

// preamble is what module's changelog opens with, ahead of its entries.
export function preamble(module: Module): string {
  return `${TITLE}\n\nEvery release of the ${module.name} module, newest first, from its release notes. Written by .github/ci/changelog.ts after each release; not edited by hand.\n`;
}

// versionOf is the version tag names, without module's prefix and the v.
function versionOf(module: Module, tag: string): string {
  return tag.slice(module.prefix.length + 1);
}

// releaseURL is tag's GitHub release in repository.
export function releaseURL(repository: string, tag: string): string {
  return `https://github.com/${repository}/releases/tag/${tag}`;
}

// entryFrom is the entry of version from its release's body: the notes
// above GitHub's generated part, or, with none, a heading linking url.
export function entryFrom(body: string, version: string, url: string): string {
  const at = body.indexOf(GENERATED_MARKER);
  const notes = (at < 0 ? body : body.slice(0, at)).trim().replace(TOP_HEADING, "## [");
  return ENTRY_HEADING.test(notes) ? notes : `## [${version}](${url})`;
}

// Entries are a changelog's entries by version, in the file's order.
type Entries = Map<string, string>;

// parseEntries is the entries of a changelog's text, refusing a text that
// does not open with TITLE or holds a version twice.
export function parseEntries(text: string): Entries {
  if (!text.startsWith(`${TITLE}\n`)) {
    throw new Error(`does not open with ${JSON.stringify(TITLE)}`);
  }
  const entries: Entries = new Map();
  let version: string | undefined;
  let body: string[] = [];
  const close = () => {
    if (version !== undefined) {
      entries.set(version, body.join("\n").trim());
    }
  };
  for (const line of text.split("\n")) {
    const heading = ENTRY_HEADING.exec(line);
    if (heading !== null) {
      close();
      const [, named = ""] = heading;
      if (entries.has(named)) {
        throw new Error(`lists ${named} twice`);
      }
      version = named;
      body = [];
    }
    body.push(line);
  }
  close();
  return entries;
}

// render is module's changelog of entries, in their order.
function render(module: Module, entries: Entries): string {
  return [preamble(module).trimEnd(), ...entries.values()].join("\n\n") + "\n";
}

// readEntries is the entries of module's changelog at root, none for a
// changelog not written yet.
function readEntries(root: string, module: Module): Entries {
  const path = join(root, changelogPath(module));
  if (!existsSync(path)) {
    return new Map();
  }
  try {
    return parseEntries(readFileSync(path, "utf8"));
  } catch (err) {
    throw new Error(`${changelogPath(module)} ${err instanceof Error ? err.message : String(err)}`);
  }
}

// checkChangelog refuses module's changelog when an entry names no release
// tag reachable from rev, or the entries are not newest first.
export async function checkChangelog(root: string, module: Module, rev: string): Promise<string> {
  const path = changelogPath(module);
  const versions = [...readEntries(root, module).keys()];
  const tags = (await releaseTagsBelow(rev, module.prefix, root)).map((t) => versionOf(module, t));
  let at = 0;
  for (const version of versions) {
    const found = tags.indexOf(version, at);
    if (found < 0) {
      const reason = tags.includes(version) ? "out of order: the newest release goes first" : `no release tag ${module.prefix}v${version} is reachable from ${rev}`;
      throw new Error(`${path} lists ${version}, ${reason}; node .github/ci/changelog.ts write rewrites it`);
    }
    at = found + 1;
  }
  const missing = tags.filter((t) => !versions.includes(t)).length;
  return `${path} lists ${versions.length} releases, each a tag, newest first${missing === 0 ? "" : `; ${missing} still to be written`}`;
}

// Writer is what writing a changelog reads releases' notes from.
export interface Writer {
  github: GitHub;
  repository: string;
  root: string;
}

// writeChangelog adds to module's changelog an entry for every release tag
// reachable from rev that it lacks and GitHub has published, and returns
// the tags it added. The entries already written stay as they are.
export async function writeChangelog(w: Writer, module: Module, rev: string): Promise<string[]> {
  const have = readEntries(w.root, module);
  const entries: Entries = new Map();
  const added: string[] = [];
  for (const tag of await releaseTagsBelow(rev, module.prefix, w.root)) {
    const version = versionOf(module, tag);
    const written = have.get(version);
    if (written !== undefined) {
      entries.set(version, written);
      continue;
    }
    const body = await w.github.releaseNotes(tag);
    if (body === undefined) {
      info(`${tag} has no GitHub release yet; its entry waits for one`);
      continue;
    }
    entries.set(version, entryFrom(body, version, releaseURL(w.repository, tag)));
    added.push(tag);
  }
  if (added.length > 0) {
    writeFileSync(join(w.root, changelogPath(module)), render(module, entries));
  }
  return added;
}

// writeAll writes every module's changelog, and returns the tags added.
export async function writeAll(w: Writer, rev: string): Promise<string[]> {
  const added: string[] = [];
  for (const name of releasedModules(w.root)) {
    added.push(...(await writeChangelog(w, selectModule(w.root, name), rev)));
  }
  return added;
}

// proposalTitle is the subject of the commit, and the title of the pull
// request, adding tags' entries.
export function proposalTitle(tags: readonly string[]): string {
  const named = tags.length <= TITLE_TAGS ? tags.join(", ") : `${tags.length} releases`;
  return `docs(changelog): add ${named}`;
}

// Context is where a proposal runs and how it pushes.
export interface Context extends Writer {
  push(branch: string): Promise<void>;
}

// proposeEntries commits every changelog with the entries it lacks on
// BRANCH, cut from main as the remote has it now with every tag, and opens
// or updates its pull request: undefined when main's changelogs lack none.
export async function proposeEntries(ctx: Context): Promise<number | undefined> {
  await git(["fetch", "--quiet", "--tags", REMOTE, BASE_BRANCH], ctx.root);
  await git(["switch", "--quiet", "--discard-changes", "-C", BRANCH, `${REMOTE}/${BASE_BRANCH}`], ctx.root);
  const added = await writeAll(ctx, "HEAD");
  if (added.length === 0) {
    info("every changelog lists every published release already");
    return undefined;
  }
  const title = proposalTitle(added);
  await git(["add", "--all", "--", `:(glob)**/${CHANGELOG}`], ctx.root);
  await git(["commit", "--quiet", "-m", title], ctx.root);
  await ctx.push(BRANCH);
  const text = { title, body: `Adds the release notes of ${added.join(", ")} to the modules' changelogs, as their GitHub releases publish them.` };
  const open = await ctx.github.sameRepoPullRequest(BRANCH, BASE_BRANCH);
  if (open !== undefined) {
    await ctx.github.editPullRequest(open, text);
    return open;
  }
  return ctx.github.createPullRequest(BRANCH, BASE_BRANCH, text);
}

// Command names what a run does.
export const Command = { Check: "check", Write: "write", Propose: "propose" } as const;

async function main(): Promise<void> {
  const [command] = process.argv.slice(2);
  const root = (await git(["rev-parse", "--show-toplevel"])).trim();
  switch (command) {
    case Command.Check:
      for (const name of releasedModules(root)) {
        info(await checkChangelog(root, selectModule(root, name), "HEAD"));
      }
      return;
    case Command.Write: {
      const repository = requireEnv("GITHUB_REPOSITORY");
      const added = await writeAll({ github: gitHubFor(requireEnv("GH_TOKEN"), repository), repository, root }, "HEAD");
      info(added.length === 0 ? "every changelog lists every published release already" : `added ${added.join(", ")}`);
      return;
    }
    case Command.Propose: {
      const token = optionalEnv("GH_TOKEN");
      if (token === undefined) {
        warning("AUTOMERGE_TOKEN is not set; the changelogs' new entries are not proposed (see release.yaml)");
        return;
      }
      const repository = requireEnv("GITHUB_REPOSITORY");
      const number = await proposeEntries({ github: gitHubFor(token, repository), repository, root, push: pushAs(token, repository, root) });
      if (number !== undefined) {
        info(`proposed the changelogs' new entries in #${number}`);
      }
      return;
    }
    default:
      throw new Error(`usage: node changelog.ts ${Object.values(Command).join(" | ")}`);
  }
}

if (import.meta.main) {
  await runMain(main);
}
