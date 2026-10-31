// What a candidate of limes's 1.0 stands on: the 0.x release of limes whose
// API it is, and each nested module's release on main when it was cut.
//
// limes stays on 0.x on main until 1.0, and the migration guide files every
// change under the 0.x release that made it, so a consumer on the candidates
// needs to know which release each candidate's API is. Its GitHub release
// says so, in a table of one row (docs/migration.md).

import { dirname } from "node:path";

import { git, lines } from "./git.ts";
import { ROOT_MODULE, releaseLine, releasedModules, selectModule } from "./modules.ts";

// NO_RELEASE fills the cell of a module with no release yet.
export const NO_RELEASE = "—";
// BETWEEN_RELEASES marks a candidate cut between two 0.x releases: the root
// module's Go code differs from the release before it.
export const BETWEEN_RELEASES = "+";
// GO_MOD marks a module's directory; GO_FILES are the files a module's API
// is in.
const GO_MOD = "go.mod";
const GO_FILES = "*.go";
// API_HEADING opens the section.
export const API_HEADING = "### API";
// TABLE_ROW opens every line of a Markdown table.
const TABLE_ROW = "|";

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

// tableLine is a Markdown table line of cells.
function tableLine(cells: readonly string[]): string {
  return `${TABLE_ROW} ${cells.join(` ${TABLE_ROW} `)} ${TABLE_ROW}`;
}

// candidateReleases is the section of a candidate's notes that names, for
// the candidate cut at rev, each module's release on its line: the root
// module's marked BETWEEN_RELEASES when the candidate's code is past it.
export async function candidateReleases(root: string, rev: string): Promise<string> {
  const modules = releasedModules(root);
  const cells: string[] = [];
  for (const name of modules) {
    const module = selectModule(root, name);
    const release = await releaseLine(module, rev, root);
    if (release === undefined) {
      cells.push(NO_RELEASE);
    } else if (name === ROOT_MODULE) {
      cells.push(`\`${release}\`${(await rootCodeChanged(root, release, rev)) ? BETWEEN_RELEASES : ""}`);
    } else {
      cells.push(`\`${release.slice(module.prefix.length)}\``);
    }
  }
  const table = [tableLine(modules), tableLine(modules.map(() => "---")), tableLine(cells)].join("\n");
  return `${API_HEADING}\n\nThis candidate's API is that of the limes release below; beside it, each nested module's release on main when it was cut. A \`${BETWEEN_RELEASES}\` marks a candidate cut past that release: it holds additions the next release makes, and none of its breaks.\n\n${table}\n`;
}
