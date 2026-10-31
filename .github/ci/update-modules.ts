// Bump every nested module's direct requirements, as deps:update bumps the
// root's, and tidy each module that builds another through a replace.
//
// Usage: node update-modules.ts
//
// deps:update moves the root module's direct requirements alone; each
// nested module — pgstore's pgx, otelmetric's OpenTelemetry, the examples',
// the tools' — has its own go.mod and would stay behind. So each module git
// tracks under the root, found from its go.mod rather than listed here,
// has its direct requirements moved to the latest minor or patch (go get -u
// does not cross a major), and each tool its go.mod names to its latest
// release, then it is tidied and built. A requirement the module
// replaces is not moved: a local replace builds the checkout's copy, and a
// replace by version pins it. A module with nothing to move is still tidied
// when it replaces a module in from the checkout, whose bump changes its
// graph too. Modules go fewer local replaces first, so one that replaces
// another in (examples replaces pgstore) is tidied after it.

import { devNull } from "node:os";
import { dirname, join } from "node:path";

import { info } from "./lib/actions.ts";
import { execute } from "./lib/exec.ts";
import { git, lines } from "./lib/git.ts";
import { runMain } from "./lib/main.ts";

// GoMod is what `go mod edit -json` says of a go.mod, as far as it is read.
interface GoMod {
  Require?: { Path: string; Indirect?: boolean }[];
  Tool?: { Path: string }[];
  Replace?: { Old: { Path: string }; New: { Path: string; Version?: string } }[];
}

// Work is what one module is due: the requirements to move, whether it
// names tools to move, and how many modules it replaces in from the
// checkout.
interface Work {
  dir: string;
  bump: string[];
  tools: boolean;
  locals: number;
}

// workOf is what the go.mod in dir is due.
function workOf(dir: string, mod: GoMod): Work {
  const replaced = new Set((mod.Replace ?? []).map((r) => r.Old.Path));
  const bump = (mod.Require ?? []).filter((r) => !r.Indirect && !replaced.has(r.Path)).map((r) => r.Path);
  // A local replace is one whose target has no version: a directory.
  const locals = (mod.Replace ?? []).filter((r) => r.New.Version === undefined).length;
  return { dir, bump, tools: (mod.Tool ?? []).length > 0, locals };
}

// Go runs go with args in dir, under the checkout's root.
export type Go = (dir: string, args: string[]) => Promise<void>;

// GO_GET_UPDATE moves requirements to their latest minor or patch.
const GO_GET_UPDATE = ["get", "-u"];
// GO_GET_TOOLS moves every tool the go.mod names to its latest release, and
// GO_BUILD_TOOLS builds them: a tool's requirements are indirect, so the
// requirements to move never name it.
const GO_GET_TOOLS = ["get", "tool"];
const GO_MOD_TIDY = ["mod", "tidy"];
// The builds write nothing: a main package built alone would leave its
// binary in the module's directory.
const GO_BUILD = ["build", "-o", devNull, "./..."];
const GO_BUILD_TOOLS = ["build", "-o", devNull, "tool"];

// updateModules moves, tidies and builds every nested module git tracks
// under root with go, and returns the directories it worked on, in order.
export async function updateModules(root: string, go: Go): Promise<string[]> {
  const due: Work[] = [];
  for (const file of lines(await git(["ls-files", "--", "*/go.mod"], root))) {
    const mod = JSON.parse(await execute("go", ["mod", "edit", "-json", join(root, file)])) as GoMod;
    const work = workOf(dirname(file), mod);
    if (work.bump.length > 0 || work.tools || work.locals > 0) {
      due.push(work);
    }
  }
  due.sort((a, b) => a.locals - b.locals || a.dir.localeCompare(b.dir));
  for (const w of due) {
    if (w.bump.length > 0) {
      await go(w.dir, [...GO_GET_UPDATE, ...w.bump]);
    }
    if (w.tools) {
      await go(w.dir, GO_GET_TOOLS);
    }
    await go(w.dir, GO_MOD_TIDY);
    if (w.bump.length > 0) {
      await go(w.dir, GO_BUILD);
    }
    if (w.tools) {
      await go(w.dir, GO_BUILD_TOOLS);
    }
    info(`update-modules: ${w.dir} ${w.bump.length > 0 || w.tools ? "moved, tidied and built" : "tidied"}`);
  }
  return due.map((w) => w.dir);
}

async function main(): Promise<void> {
  if (process.argv.length > 2) {
    throw new Error("usage: update-modules.ts");
  }
  const root = (await git(["rev-parse", "--show-toplevel"])).trim();
  await updateModules(root, async (dir, args) => void (await execute("go", ["-C", join(root, dir), ...args])));
}

if (import.meta.main) {
  await runMain(main);
}
