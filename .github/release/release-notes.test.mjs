// release-notes.test.mjs — the notes a release is published with, through the
// generator and the plugin config release.config.cjs gives them.
//
// A breaking change missing from the notes is silent: the release is cut and
// published, and its readers learn of the break from their compiler. The
// cases below pin that every breaking commit is listed, and that the notes
// reach the file release.yaml publishes them from.
//
// Runs from `task verify-all` after `npm ci`.

import { createRequire } from "node:module";
import { mkdtempSync, readFileSync, existsSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { generateNotes } from "@semantic-release/release-notes-generator";

const require = createRequire(import.meta.url);
const config = require("./release.config.cjs");
const notesFile = require("./release-notes-file.cjs");

const BREAKING_HEADING = "BREAKING CHANGES";

const commits = [
  { hash: "1111111", message: "fix(dpop)!: read proofs as the RFCs define them" },
  { hash: "2222222", message: "feat: add a caveat\n\nBREAKING CHANGE: Seen takes the key thumbprint" },
  { hash: "3333333", message: "fix(jwks): keep the set when no key is usable" },
  { hash: "4444444", message: "docs: explain a thing" },
];

// [text the notes must hold, why]
const wanted = [
  [BREAKING_HEADING, "breaking commits get their own section"],
  ["read proofs as the RFCs define them", "a `!` subject is listed as breaking"],
  ["Seen takes the key thumbprint", "a BREAKING CHANGE footer is listed"],
  ["Features", "feat is listed"],
  ["Bug Fixes", "fix is listed"],
  ["keep the set when no key is usable", "a plain fix is listed"],
];

const logger = { log() {}, error() {}, warn() {}, success() {} };
const context = {
  commits,
  logger,
  cwd: process.cwd(),
  env: {},
  options: { repositoryUrl: config.repositoryUrl },
  lastRelease: { gitTag: "v0.4.0", version: "0.4.0" },
  nextRelease: { gitTag: "v0.5.0", version: "0.5.0", type: "minor" },
};

let failed = 0;
const notes = await generateNotes(config.notes, context);
for (const [text, why] of wanted) {
  if (!notes.includes(text)) {
    console.log(`FAIL notes lack ${JSON.stringify(text)}: ${why}`);
    failed = 1;
  }
}

const names = config.plugins.map((p) => (Array.isArray(p) ? p[0] : p));
const generatorAt = names.indexOf("@semantic-release/release-notes-generator");
const fileAt = names.indexOf("./release-notes-file.cjs");
if (generatorAt < 0 || fileAt < 0) {
  console.log("FAIL the config must load the notes generator and the plugin that writes its notes");
  failed = 1;
}

// The plugin writes the notes where release.yaml reads them.
const dir = mkdtempSync(join(tmpdir(), "release-notes-"));
try {
  const path = join(dir, "notes.md");
  await notesFile.prepare({}, { ...context, env: { [notesFile.NOTES_FILE_ENV]: path }, nextRelease: { ...context.nextRelease, notes } });
  if (!existsSync(path) || readFileSync(path, "utf8") !== notes) {
    console.log(`FAIL prepare did not write the notes to ${notesFile.NOTES_FILE_ENV}`);
    failed = 1;
  }
  // Without the variable it writes nothing and does not fail the release.
  await notesFile.prepare({}, { ...context, nextRelease: { ...context.nextRelease, notes } });
} finally {
  rmSync(dir, { recursive: true, force: true });
}

if (failed) process.exit(1);
console.log("release notes: every breaking change is listed");
