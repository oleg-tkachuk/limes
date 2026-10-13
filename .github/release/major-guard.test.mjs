// major-guard.test.mjs — the release refuses a major the module path does
// not carry, and lets every other version through.
//
// Runs from `task verify-all`.

import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const guard = require("./major-guard.cjs");
const modules = require("./modules.cjs");

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
// A module whose release line nobody seeded is refused, not released as 1.0.0.
for (const [lastRelease, refused] of [[{}, true], [undefined, true], [{ version: "0.1.0" }, false]]) {
  let got = false;
  try {
    guard.checkFirst(lastRelease, "sub/");
  } catch {
    got = true;
  }
  if (got !== refused) {
    console.log(`FAIL checkFirst(${JSON.stringify(lastRelease)}): refused=${got}, want ${refused}`);
    failures++;
  }
}

// Every module's config runs the guard against its own go.mod, which parses
// and allows the release line it is on.
const { readFileSync } = await import("node:fs");
for (const name of Object.keys(modules.MODULES)) {
  const cfg = modules.configFor(name);
  const entry = cfg.plugins.find((p) => Array.isArray(p) && p[0] === "./major-guard.cjs");
  if (!entry) {
    console.log(`FAIL the ${name} release does not run major-guard.cjs`);
    failures++;
    continue;
  }
  const own = guard.modulePath(readFileSync(guard.goModOf(entry[1].dir), "utf8"));
  const want = `${bare}${modules.MODULES[name].dir ? `/${modules.MODULES[name].dir}` : ""}`;
  if (own !== want) {
    console.log(`FAIL the ${name} release guards ${own}, want ${want}`);
    failures++;
  }
  if (guard.moduleMajor(own) < 1) {
    console.log(`FAIL the module path ${own} allows no release`);
    failures++;
  }
  if (entry[1].tagPrefix !== modules.tagPrefix(modules.MODULES[name].dir)) {
    console.log(`FAIL the ${name} release names the seed tag with prefix ${entry[1].tagPrefix}`);
    failures++;
  }
}

if (failures > 0) {
  process.exit(1);
}
console.log("major-guard: every case matches");
