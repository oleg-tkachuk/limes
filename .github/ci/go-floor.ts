// Is the oldest Go CI tests on a patch of go.mod's line?
//
// Usage: node go-floor.ts <GO_FLOOR_VERSION>, from the module's directory,
// whose go.mod's go directive it reads.
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

// goDirective is the go directive of the go.mod in cwd.
async function goDirective(): Promise<string> {
  return (JSON.parse(await execute("go", ["mod", "edit", "-json"])) as { Go: string }).Go;
}

async function main(): Promise<void> {
  const [floor, ...rest] = process.argv.slice(2);
  if (floor === undefined || rest.length > 0) {
    throw new Error("usage: go-floor.ts <floor>");
  }
  const directive = await goDirective();
  if (!floorOnLine(floor, directive)) {
    throw new Error(`GO_FLOOR_VERSION ${floor} is not a patch of the go.mod directive ${directive}`);
  }
}

if (import.meta.main) {
  await runMain(main);
}
