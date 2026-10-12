// major-guard.test.mjs — the release refuses a major the module path does
// not carry, and lets every other version through.
//
// Runs from `task verify-all`.

import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const guard = require("./major-guard.cjs");
const config = require("./release.config.cjs");

const bare = "github.com/oleg-tkachuk/limes";
// [version, module path, refused]
const cases = [
  ["0.42.0", bare, false],
  ["1.0.0", bare, false],
  ["1.7.3", bare, false],
  ["2.0.0", bare, true],
  ["2.0.0", `${bare}/v2`, false],
  ["3.0.0", `${bare}/v2`, true],
];

let failures = 0;
for (const [version, path, refused] of cases) {
  let got = false;
  try {
    guard.check(version, path);
  } catch {
    got = true;
  }
  if (got !== refused) {
    console.log(`FAIL v${version} on ${path}: refused=${got}, want ${refused}`);
    failures++;
  }
}

if (guard.modulePath("module example.com/m/v3\n\ngo 1.26\n") !== "example.com/m/v3") {
  console.log("FAIL modulePath does not read the module directive");
  failures++;
}
// The guard is useless unless the release runs it.
if (!config.plugins.includes("./major-guard.cjs")) {
  console.log("FAIL release.config.cjs does not run major-guard.cjs");
  failures++;
}
// The repository's own go.mod parses, and allows the release line it is on.
const { readFileSync } = await import("node:fs");
const own = guard.modulePath(readFileSync(new URL("../../go.mod", import.meta.url), "utf8"));
if (guard.moduleMajor(own) < 1) {
  console.log(`FAIL the module path ${own} allows no release`);
  failures++;
}

if (failures > 0) {
  process.exit(1);
}
console.log("major-guard: every case matches");
