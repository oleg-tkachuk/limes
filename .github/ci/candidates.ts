// The table of limes's 1.0 candidates in the migration guide, held to the
// tags.
//
// Usage: node candidates.ts check | write
//        GH_TOKEN=… GITHUB_REPOSITORY=… node candidates.ts propose <candidate tag>
//
// limes stays on 0.x on main until 1.0, and the guide files every change
// under the 0.x release that made it, so a consumer on the candidates needs
// to know which release each candidate's API is: the table says, with each
// nested module's release on main when the candidate was cut. Every row
// follows from the tags, so none is written by hand. check fails when the
// table is not the one the candidates reachable from HEAD make — a pull
// request cut before a candidate does not see it, one cut after does;
// write rewrites it so. propose, run after a candidate is cut, commits the
// rewritten table on BRANCH cut from main and opens or updates its pull
// request, which auto-merge.yaml merges once green. Without GH_TOKEN it
// warns and does nothing, as require-limes.ts does, and for the same reason.

import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";

import { info, warning } from "./lib/actions.ts";
import { optionalEnv, requireEnv } from "./lib/env.ts";
import { git, lines } from "./lib/git.ts";
import { type GitHub, gitHubFor } from "./lib/github.ts";
import { runMain } from "./lib/main.ts";
import { ROOT_MODULE, releaseLine, releasedModules, selectModule } from "./lib/modules.ts";
import { RC, STABLE_MAJOR } from "./lib/prerelease.ts";
import { MIGRATION_GUIDE } from "./release-checks.ts";
import { pushAs } from "./lib/push.ts";
import { BASE_BRANCH } from "./merge-policy.ts";

// CANDIDATE_VERSION is the version every candidate of limes's 1.0 is of,
// up to its prerelease, and CANDIDATE_PREFIX starts its tag up to the number.
const CANDIDATE_VERSION = `v${STABLE_MAJOR}.0.0-`;
export const CANDIDATE_PREFIX = `${CANDIDATE_VERSION}${RC}.`;
// CANDIDATE_HEADING heads the table's first column; the table starts at the
// line that opens with it.
export const CANDIDATE_HEADING = "Candidate";
// NO_RELEASE fills the cell of a module with no release yet.
export const NO_RELEASE = "—";
// BETWEEN_RELEASES marks a candidate cut between two 0.x releases: the root
// module's Go code differs from the release before it.
export const BETWEEN_RELEASES = "+";
// BRANCH is the branch propose commits the table on.
export const BRANCH = "docs/migration-candidates";
// GO_MOD marks a module's directory; GO_FILES are the files a module's API
// is in.
const GO_MOD = "go.mod";
const GO_FILES = "*.go";
const REMOTE = "origin";
// TABLE_ROW opens every line of a Markdown table.
const TABLE_ROW = "|";

// candidateTags is every candidate of limes's 1.0 reachable from rev, the
// newest first.
export async function candidateTags(root: string, rev: string): Promise<string[]> {
  const tags = lines(await git(["tag", "--merged", rev, "--list", `${CANDIDATE_PREFIX}*`], root));
  const number = (t: string) => Number(t.slice(CANDIDATE_PREFIX.length));
  return tags.filter((t) => Number.isInteger(number(t))).sort((a, b) => number(b) - number(a));
}

// moduleDirs is the directory of every module at rev other than the root:
// each one a go.mod is in, released or not.
async function moduleDirs(root: string, rev: string): Promise<string[]> {
  return lines(await git(["ls-tree", "-r", "--name-only", rev], root))
    .filter((p) => p.endsWith(`/${GO_MOD}`))
    .map((p) => dirname(p));
}

// rootCodeChanged reports whether the root module's Go code differs between
// from and to.
async function rootCodeChanged(root: string, from: string, to: string): Promise<boolean> {
  const excluded = (await moduleDirs(root, to)).map((d) => `:(exclude)${d}`);
  return lines(await git(["diff", "--name-only", from, to, "--", GO_FILES, ...excluded], root)).length > 0;
}

// cell is a value as the table shows it.
function cell(value: string): string {
  return `\`${value}\``;
}

// tableLine is a Markdown table line of cells.
function tableLine(cells: readonly string[]): string {
  return `${TABLE_ROW} ${cells.join(` ${TABLE_ROW} `)} ${TABLE_ROW}`;
}

// candidateRow is tag's row: the root module's release before it, marked
// when the candidate's code is past it, and each nested module's release.
export async function candidateRow(root: string, tag: string): Promise<string> {
  const cells = [cell(tag.slice(CANDIDATE_VERSION.length))];
  for (const name of releasedModules(root)) {
    const module = selectModule(root, name);
    const release = await releaseLine(module, tag, root);
    if (release === undefined) {
      cells.push(NO_RELEASE);
    } else if (name === ROOT_MODULE) {
      cells.push(cell(release) + ((await rootCodeChanged(root, release, tag)) ? BETWEEN_RELEASES : ""));
    } else {
      cells.push(cell(release.slice(module.prefix.length)));
    }
  }
  return tableLine(cells);
}

// candidatesTable is the table of the candidates reachable from rev.
export async function candidatesTable(root: string, rev: string): Promise<string> {
  const modules = releasedModules(root);
  const rows = [tableLine([CANDIDATE_HEADING, ...modules]), tableLine([CANDIDATE_HEADING, ...modules].map(() => "---"))];
  for (const tag of await candidateTags(root, rev)) {
    rows.push(await candidateRow(root, tag));
  }
  return rows.join("\n");
}

// tableSpan is where the guide's table is: its first line and the line
// past its last.
function tableSpan(guide: string[]): [number, number] {
  const start = guide.findIndex((l) => l.startsWith(`${TABLE_ROW} ${CANDIDATE_HEADING} `));
  if (start < 0) {
    throw new Error(`${MIGRATION_GUIDE} has no table of candidates: no line opens with "${TABLE_ROW} ${CANDIDATE_HEADING}"`);
  }
  const past = guide.findIndex((l, i) => i > start && !l.startsWith(TABLE_ROW));
  return [start, past < 0 ? guide.length : past];
}

// guideTable is the table guide holds.
export function guideTable(guide: string): string {
  const all = guide.split("\n");
  const [start, past] = tableSpan(all);
  return all.slice(start, past).join("\n");
}

// withTable is guide with table in place of the one it holds.
export function withTable(guide: string, table: string): string {
  const all = guide.split("\n");
  const [start, past] = tableSpan(all);
  return [...all.slice(0, start), table, ...all.slice(past)].join("\n");
}

// checkTable refuses a guide whose table is not the one the candidates
// reachable from rev make, naming the first line that differs.
export async function checkTable(root: string, rev: string): Promise<string> {
  const want = await candidatesTable(root, rev);
  const have = guideTable(readFileSync(join(root, MIGRATION_GUIDE), "utf8"));
  if (have === want) {
    return `${MIGRATION_GUIDE}'s table of candidates holds every candidate reachable from ${rev}`;
  }
  const wantLines = want.split("\n");
  const haveLines = have.split("\n");
  let at = 0;
  while (wantLines[at] === haveLines[at]) {
    at++;
  }
  const quote = (l: string | undefined) => (l === undefined ? "nothing" : JSON.stringify(l));
  throw new Error(
    `${MIGRATION_GUIDE}'s table of candidates is not the tags': it reads ${quote(haveLines[at])} where the tags make ${quote(wantLines[at])}; node .github/ci/candidates.ts write rewrites it`,
  );
}

// writeTable rewrites the guide's table to the candidates reachable from
// rev, and reports whether it changed.
export async function writeTable(root: string, rev: string): Promise<boolean> {
  const path = join(root, MIGRATION_GUIDE);
  const guide = readFileSync(path, "utf8");
  const updated = withTable(guide, await candidatesTable(root, rev));
  if (updated === guide) {
    return false;
  }
  writeFileSync(path, updated);
  return true;
}

// rowTitle is the subject of the commit, and the title of the pull
// request, adding tag's row.
export function rowTitle(tag: string): string {
  return `docs(migration): add ${tag} to the table of candidates`;
}

// Context is where a proposal runs and how it pushes.
export interface Context {
  github: GitHub;
  root: string;
  push(branch: string): Promise<void>;
}

// proposeRow commits the table with tag's row on BRANCH, cut from main as
// the remote has it now with every tag, and opens or updates its pull
// request: undefined when main's table has the row already.
export async function proposeRow(ctx: Context, tag: string): Promise<number | undefined> {
  if (!tag.startsWith(CANDIDATE_PREFIX)) {
    throw new Error(`${JSON.stringify(tag)} is not a candidate of limes's ${STABLE_MAJOR}.0`);
  }
  await git(["fetch", "--quiet", "--tags", REMOTE, BASE_BRANCH], ctx.root);
  await git(["switch", "--quiet", "--discard-changes", "-C", BRANCH, `${REMOTE}/${BASE_BRANCH}`], ctx.root);
  if (!(await writeTable(ctx.root, "HEAD"))) {
    info(`${MIGRATION_GUIDE} lists ${tag} already`);
    return undefined;
  }
  const title = rowTitle(tag);
  await git(["commit", "--quiet", "-m", title, "--", MIGRATION_GUIDE], ctx.root);
  await ctx.push(BRANCH);
  const text = { title, body: `${tag} is cut. This adds its row to the table of candidates in ${MIGRATION_GUIDE}, which CI holds to the tags.` };
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
  const [command, tag = ""] = process.argv.slice(2);
  const root = (await git(["rev-parse", "--show-toplevel"])).trim();
  switch (command) {
    case Command.Check:
      info(await checkTable(root, "HEAD"));
      return;
    case Command.Write:
      info((await writeTable(root, "HEAD")) ? `${MIGRATION_GUIDE}'s table of candidates rewritten` : `${MIGRATION_GUIDE}'s table of candidates is the tags' already`);
      return;
    case Command.Propose: {
      const token = optionalEnv("GH_TOKEN");
      if (token === undefined) {
        warning(`AUTOMERGE_TOKEN is not set; ${tag}'s row in ${MIGRATION_GUIDE} is not proposed (see release.yaml)`);
        return;
      }
      const repository = requireEnv("GITHUB_REPOSITORY");
      const number = await proposeRow({ github: gitHubFor(token, repository), root, push: pushAs(token, repository, root) }, tag);
      if (number !== undefined) {
        info(`proposed ${tag}'s row in #${number}`);
      }
      return;
    }
    default:
      throw new Error(`usage: node candidates.ts ${Object.values(Command).join(" | ")}`);
  }
}

if (import.meta.main) {
  await runMain(main);
}
