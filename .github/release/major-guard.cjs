// A semantic-release plugin of one step: refuse a release whose major version
// the module path does not carry.
//
// A Go module's import path names its major from v2 on (…/v2), so a tag
// v2.0.0 on a module whose path has no /v2 is one the Go proxy ignores, and
// every later release counts from it. semantic-release cuts a major for any
// breaking marker it reads — in a subject, a footer in any case, a merge
// commit's body — so the check sits here, on the one path every release
// takes, rather than only on the pull request.

const fs = require("node:fs");
const path = require("node:path");

// The module's go.mod, two levels above this directory.
const GO_MOD = path.join(__dirname, "..", "..", "go.mod");
// The major a module path without a /vN suffix may release: v0 and v1 share
// the bare path.
const BARE_PATH_MAJOR = 1;

// moduleMajor is the highest major the module path allows.
function moduleMajor(modulePath) {
  const m = /\/v(\d+)$/.exec(modulePath);
  return m ? Number(m[1]) : BARE_PATH_MAJOR;
}

// modulePath reads the module directive of a go.mod.
function modulePath(goMod) {
  const m = /^module\s+(\S+)/m.exec(goMod);
  if (!m) {
    throw new Error("go.mod has no module directive");
  }
  return m[1];
}

// check throws when version's major is past what modulePath allows.
function check(version, modPath) {
  const major = Number(version.split(".")[0]);
  const allowed = moduleMajor(modPath);
  if (major > allowed) {
    throw new Error(
      `refusing v${version}: module ${modPath} releases up to v${allowed}.x; ` +
        `a breaking change past it needs the /v${major} module path first`,
    );
  }
}

async function verifyRelease(_pluginConfig, { nextRelease }) {
  check(nextRelease.version, modulePath(fs.readFileSync(GO_MOD, "utf8")));
}

module.exports = { verifyRelease, check, moduleMajor, modulePath };
