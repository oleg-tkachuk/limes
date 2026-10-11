// A semantic-release plugin of one step: write the notes the generator built
// for this release to the file RELEASE_NOTES_FILE names, for release.yaml to
// publish with the GitHub release. The notes come from the commits, so a
// breaking change is listed whether or not its pull request was labelled.
//
// No @semantic-release/github instead: the tag is the release, and the
// workflow publishes the GitHub release itself — see release.config.cjs.

const fs = require("node:fs");

const NOTES_FILE_ENV = "RELEASE_NOTES_FILE";

// prepare runs once the next version and its notes are known, before the tag
// is pushed. Without the variable — a local run — nothing is written.
async function prepare(_pluginConfig, { env, nextRelease, logger }) {
  const path = env[NOTES_FILE_ENV];
  if (!path) {
    logger.log(`${NOTES_FILE_ENV} is not set; the release notes are not written`);
    return;
  }
  fs.writeFileSync(path, nextRelease.notes ?? "");
}

module.exports = { prepare, NOTES_FILE_ENV };
