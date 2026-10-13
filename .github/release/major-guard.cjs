// A semantic-release plugin of one step: refuse a release whose major version
// the module path does not carry, or a module's first release.
//
// A Go module's import path names its major from v2 on (…/v2), so a tag
// v2.0.0 on a module whose path has no /v2 is one the Go proxy ignores, and
// every later release counts from it. semantic-release cuts a major for any
// breaking marker it reads — in a subject, a footer in any case, a merge
// commit's body — so the check sits here, on the one path every release
// takes, rather than only on the pull request.
//
// A module with no release tag yet would be released as 1.0.0, semantic-
// release's first version, which promises a stable API by accident. Its
// release line starts from a tag pushed by hand instead, and this refuses to
// start it for one.

const fs = require("node:fs");
const path = require("node:path");

// The repository root, two levels above this directory.
const REPO_ROOT = path.join(__dirname, "..", "..");
const GO_MOD = "go.mod";
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

// checkFirst throws when there is no last release to count from: the release
// line of the module whose tags start with prefix has not been seeded.
function checkFirst(lastRelease, prefix) {
  if (!lastRelease?.version) {
    throw new Error(
      `refusing a first release, which semantic-release cuts as 1.0.0: ` +
        `seed the release line with a ${prefix}v0.x.y tag on a commit that holds the module`,
    );
  }
}

// goModOf is the path of the go.mod of the module in dir, empty for the root.
function goModOf(dir) {
  return path.join(REPO_ROOT, dir ?? "", GO_MOD);
}

async function verifyRelease(pluginConfig, { lastRelease, nextRelease }) {
  checkFirst(lastRelease, pluginConfig.tagPrefix ?? "");
  check(nextRelease.version, modulePath(fs.readFileSync(goModOf(pluginConfig.dir), "utf8")));
}

module.exports = { verifyRelease, check, checkFirst, goModOf, moduleMajor, modulePath };
