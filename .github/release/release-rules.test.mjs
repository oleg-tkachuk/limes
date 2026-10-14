// release-rules.test.mjs — which release each commit cuts, through the
// analyzer and the plugin config release.config.cjs gives it.
//
// A commit that releases nothing is silent: the release workflow succeeds and
// no version is cut. Each case below pins one commit type.
//
// Runs from `task verify-all` after `npm ci`.

import { createRequire } from "node:module";
import { analyzeCommits } from "@semantic-release/commit-analyzer";

const require = createRequire(import.meta.url);
const config = require("./release.config.cjs");
const modules = require("./modules.cjs");

// [commit message, release wanted]
const cases = [
  ["feat: add a caveat", "minor"],
  ["feat(dpop): accept a P-384 key", "minor"],
  ["fix: repair a check", "patch"],
  ["perf: verify faster", "patch"],
  ["security: refuse a forged proof", "patch"],
  // Pre-1.0, a breaking change is a minor.
  ["feat!: rename a request field", "minor"],
  ["fix: drop a field\n\nBREAKING CHANGE: the field is gone", "minor"],
  ["docs: explain a thing", null],
  ["chore: tidy", null],
  ["refactor: move a thing", null],
  ["test: cover a thing", null],
  ["build: pin the toolchain", null],
  ["ci: cache the module", null],
];

// Cases of one module's own rules, beside the shared ones above.
const moduleCases = {
  // pgstore releases nothing of the limes module, on a line of its own.
  [modules.ROOT_MODULE]: [
    ["feat(pgstore): add the meter", null],
    ["fix(pgstore): lock in order", null],
    // So does redisreplay, on a line of its own.
    ["feat(redisreplay): add a script", null],
    ["fix(redisreplay): expire the key", null],
    // A nested module's build change releases that module, not limes.
    ["build(pgstore): require limes v0.64.0", null],
    ["build(redisreplay): require limes v0.64.0", null],
  ],
  // A nested module's own build change is a patch of it: the go.mod its users
  // resolve has changed. The first is the subject require-released-limes.sh
  // commits, which once merged and released nothing.
  [modules.REDISREPLAY_MODULE]: [
    ["build(redisreplay): require limes v0.64.0", "patch"],
    ["build(redisreplay): bump go-redis", "patch"],
    ["build(pgstore): require limes v0.64.0", null],
  ],
  [modules.PGSTORE_MODULE]: [
    ["build(pgstore): require limes v0.64.0", "patch"],
    ["build(redisreplay): require limes v0.64.0", null],
  ],
};

const logger = { log() {}, error() {}, warn() {}, success() {} };
let failed = 0;

// Every module releases by the same rules today, each from its own config,
// and tags as the Go proxy reads it: vX.Y.Z at the root, dir/vX.Y.Z for a
// module in dir.
for (const [name, module] of Object.entries(modules.MODULES)) {
  const cfg = modules.configFor(name);
  for (const [message, want] of [...cases, ...(moduleCases[name] ?? [])]) {
    const got = await analyzeCommits(cfg.analyzer, {
      commits: [{ hash: "0", message }],
      logger,
      cwd: process.cwd(),
      options: {},
    });
    if ((got ?? null) !== want) {
      console.log(`FAIL ${name} ${JSON.stringify(message)}: got ${got ?? null}, want ${want}`);
      failed = 1;
    }
  }
  const prefix = module.dir === "" ? "" : `${module.dir}/`;
  if (cfg.tagFormat !== `${prefix}v\${version}`) {
    console.log(`FAIL ${name} tagFormat ${cfg.tagFormat}: the Go proxy reads ${prefix}vX.Y.Z`);
    failed = 1;
  }
}

// The config semantic-release loads is the root module's unless
// RELEASE_MODULE names another.
if (config.tagFormat !== modules.configFor(modules.ROOT_MODULE).tagFormat) {
  console.log(`FAIL release.config.cjs releases ${config.tagFormat} with ${modules.MODULE_ENV} unset`);
  failed = 1;
}
// A module here that release.yaml never releases would never be tagged, and
// nothing would say so: every nested one must be named there.
const { readFileSync } = await import("node:fs");
const workflow = readFileSync(new URL("../workflows/release.yaml", import.meta.url), "utf8");
for (const [name, module] of Object.entries(modules.MODULES)) {
  if (module.dir !== "" && !new RegExp(`^\\s+[A-Z_]+_MODULE: ${name}$`, "m").test(workflow)) {
    console.log(`FAIL release.yaml does not release the ${name} module`);
    failed = 1;
  }
}

let unknownRefused = false;
try {
  modules.configFor("no-such-module");
} catch {
  unknownRefused = true;
}
if (!unknownRefused) {
  console.log("FAIL a module name no config holds was accepted");
  failed = 1;
}

// package.json replaces @semantic-release/npm with a stub that throws: it
// bundles the npm CLI and a dependency with an unfixed advisory, and this
// repository publishes nothing to npm. That holds only while the config names
// its plugins — semantic-release falls back to a default list holding the npm
// plugin when none are given — and none of them is the npm plugin.
const NPM_PLUGIN = "@semantic-release/npm";
const names = (config.plugins ?? []).map((p) => (Array.isArray(p) ? p[0] : p));
if (names.length === 0) {
  console.log("FAIL names no plugins, so semantic-release would load its defaults");
  failed = 1;
}
if (names.includes(NPM_PLUGIN)) {
  console.log(`FAIL loads ${NPM_PLUGIN}, which package.json stubs out`);
  failed = 1;
}
try {
  require(NPM_PLUGIN);
  console.log(`FAIL ${NPM_PLUGIN} loaded: the stub that replaces it must refuse`);
  failed = 1;
} catch {
  // the stub refused, as it should
}

if (failed) process.exit(1);
console.log("release rules: every commit cuts the release it should");
