// A semantic-release plugin of one step: write the notes the generator built
// for this release to the file RELEASE_NOTES_FILE names, for release.yaml to
// publish with the GitHub release. The notes come from the commits, so a
// breaking change is listed whether or not its pull request was labelled.
//
// No @semantic-release/github instead: the tag is the release, and the
// workflow publishes the GitHub release itself — see release.config.cjs.

const fs = require("node:fs");

const NOTES_FILE_ENV = "RELEASE_NOTES_FILE";

// write puts the notes in the file RELEASE_NOTES_FILE names. Without the
// variable — a local run — nothing is written.
function write({ env, nextRelease, logger }) {
  const path = env[NOTES_FILE_ENV];
  if (!path) {
    logger.log(`${NOTES_FILE_ENV} is not set; the release notes are not written`);
    return;
  }
  fs.writeFileSync(path, nextRelease.notes ?? "");
}

// generateNotes runs after the plugins listed before this one generated
// theirs, which it is handed, and in a dry run too, which prepare is not:
// a release candidate's notes come from a dry run (.github/ci/release.ts).
// It adds nothing to the notes. What it writes has not been through
// semantic-release's masking of secrets yet, which runs after every plugin
// generated its notes; a release's are then written again, masked, by
// prepare, but a candidate's stay as written here — commit messages carry
// no secrets, and nothing runs after the masking in a dry run. Their heading
// is the release's version, linked to a comparison that exists once that
// release is tagged.
async function generateNotes(_pluginConfig, context) {
  write(context);
  return "";
}

// prepare runs once the next version and its notes are known, before the tag
// is pushed: the notes a release is published with.
async function prepare(_pluginConfig, context) {
  write(context);
}

module.exports = { generateNotes, prepare, NOTES_FILE_ENV };
