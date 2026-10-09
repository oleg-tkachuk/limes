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

const logger = { log() {}, error() {}, warn() {}, success() {} };
let failed = 0;
for (const [message, want] of cases) {
  const got = await analyzeCommits(config.analyzer, {
    commits: [{ hash: "0", message }],
    logger,
    cwd: process.cwd(),
    options: {},
  });
  if ((got ?? null) !== want) {
    console.log(`FAIL ${JSON.stringify(message)}: got ${got ?? null}, want ${want}`);
    failed = 1;
  }
}

if (!config.tagFormat.startsWith("v") || !config.tagFormat.includes("${version}")) {
  console.log(`FAIL tagFormat ${config.tagFormat}: the Go proxy reads vX.Y.Z at the module root`);
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
