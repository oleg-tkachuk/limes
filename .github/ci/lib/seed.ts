// The start of a nested module's release line.

import { join } from "node:path";

import { git } from "./git.ts";
import { ROOT_MODULE, type Module } from "./modules.ts";

// SEED_VERSION is where a nested module's release line starts: semantic-
// release cuts a module with no tag as 1.0.0, which major-guard refuses.
export const SEED_VERSION = "v0.1.0";

// GO_MOD is the file whose first commit holds a module.
const GO_MOD = "go.mod";

// seedLine starts the release line of a nested module no tag names yet,
// with a SEED_VERSION tag on the commit that added its go.mod, pushed with
// the deploy key, which alone may create a release tag. It returns the tag,
// or undefined for the root module, one already tagged, or one whose go.mod
// no commit added.
export async function seedLine(root: string, module: Module, pushTag: (tag: string) => Promise<void>): Promise<string | undefined> {
  if (module.name === ROOT_MODULE || (await git(["tag", "--list", `${module.prefix}v*`], root)).trim() !== "") {
    return undefined;
  }
  const added = (await git(["log", "--diff-filter=A", "--format=%H", "--", join(module.dir, GO_MOD)], root)).trim().split("\n").at(-1);
  if (!added) {
    return undefined;
  }
  const tag = `${module.prefix}${SEED_VERSION}`;
  await git(["tag", tag, added], root);
  await pushTag(tag);
  return tag;
}
