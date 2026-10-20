// Is the oldest Go CI tests on a patch of each tested module's go.mod line?
//
// Usage: node go-floor.ts <GO_FLOOR_VERSION> [module directory ...], from
// the repository's root; with no directory, the root module alone. Each
// directory's go.mod go directive is read: a nested module the floor job
// tests binds its consumers by its own directive, not the root's.
//
// Both name a Go release line by their first two parts — 1.26 of 1.26.9 and
// of 1.26.0, or of a directive written 1.26 — so the floor is a patch of the
// directive exactly when those agree. A directive may carry two parts or
// three; comparing what is left after the last dot would read "1.26" as 1.

import { execute } from "./lib/exec.ts";
import { runMain } from "./lib/main.ts";

// LINE_PARTS is how many dot-separated parts name a release line.
const LINE_PARTS = 2;

// releaseLine is a version's first two dot-separated parts.
export function releaseLine(version: string): string {
  return version.split(".").slice(0, LINE_PARTS).join(".");
}

// floorOnLine reports whether floor is a patch of directive's release line.
export function floorOnLine(floor: string, directive: string): boolean {
  return releaseLine(floor) === releaseLine(directive);
}

// ROOT_DIR is the root module's directory, the one checked by default.
const ROOT_DIR = ".";

// offFloor is each directory whose directive floor is no patch of, with its
// directive.
export function offFloor(floor: string, directives: Readonly<Record<string, string>>): string[] {
  return Object.entries(directives)
    .filter(([, directive]) => !floorOnLine(floor, directive))
    .map(([dir, directive]) => `${dir} (go ${directive})`);
}

// goDirective is the go directive of the go.mod in dir.
async function goDirective(dir: string): Promise<string> {
  return (JSON.parse(await execute("go", ["mod", "edit", "-json"], { cwd: dir })) as { Go: string }).Go;
}

async function main(): Promise<void> {
  const [floor, ...dirs] = process.argv.slice(2);
  if (floor === undefined) {
    throw new Error("usage: go-floor.ts <floor> [module directory ...]");
  }
  const directives: Record<string, string> = {};
  for (const dir of dirs.length > 0 ? dirs : [ROOT_DIR]) {
    directives[dir] = await goDirective(dir);
  }
  const off = offFloor(floor, directives);
  if (off.length > 0) {
    throw new Error(`GO_FLOOR_VERSION ${floor} is not a patch of the go directive of ${off.join(", ")}`);
  }
}

if (import.meta.main) {
  await runMain(main);
}
