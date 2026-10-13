// The modules this repository releases, and the semantic-release config of
// each; release.config.cjs picks one.
//
// A release computes the next SemVer from Conventional Commits, writes the
// release notes and creates the git tag — nothing else. Run by
// .github/workflows/release.yaml, which ci.yaml dispatches once every check
// on a push to main has passed.
//
// The tag IS the release: the Go proxy reads it. The GitHub release beside it
// is notes for people, created by the workflow with `gh release create`: the
// notes generated here from the commits — breaking changes first — and below
// them GitHub's list of merged pull requests, shaped by .github/release.yml.
// So no @semantic-release/github here; release-notes-file.cjs hands the notes
// over. release-notes.test.mjs pins them.
//
// The repository holds more than one Go module, each released on its own
// line: MODULES below, picked by the RELEASE_MODULE variable, the root
// module when it is unset. module-commits.cjs hands each module's analyzer
// and notes only the commits that touch it, so a change to one module cuts
// no release of another.
//
// `conventionalcommits` preset: feat is a minor, fix and perf are a patch,
// and docs/style/refactor/test/build/ci/chore release nothing. `security` is a
// patch: the preset knows no such type. Every module is pre-1.0, where a
// breaking change is a minor; that rule goes from a module's own rules when
// it reaches 1.0. release-rules.test.mjs pins all of it. major-guard.cjs
// refuses a major the module path does not carry, whatever marker cut it.

const PRESET = "conventionalcommits";
const SECURITY_IS_A_PATCH = { type: "security", release: "patch" };
const BREAKING_IS_A_MINOR_BEFORE_1_0 = { breaking: true, release: "minor" };
// A pgstore or redisreplay commit cuts no release of the limes module. Their
// directories are left out of its commits already (UNRELEASED_DIRS, and
// redisreplay's own line); these cover a commit scoped to one that also
// touches a path outside it. A breaking change there would still cut one —
// the rule above matches it too — so such a commit carries no breaking
// marker.
const PGSTORE_RELEASES_NOTHING = { scope: "pgstore", release: false };
const REDISREPLAY_RELEASES_NOTHING = { scope: "redisreplay", release: false };

// The variable that picks the module to release, and the one picked without it.
const MODULE_ENV = "RELEASE_MODULE";
const ROOT_MODULE = "limes";

// Each Go module this repository releases. dir is the module's directory from
// the repository root, empty for the root module; a nested module's tags carry
// it as a prefix, as the Go proxy requires. releaseRules are the module's own,
// so one module reaching 1.0 does not change how another releases.
const MODULES = {
  [ROOT_MODULE]: {
    dir: "",
    releaseRules: [
      SECURITY_IS_A_PATCH,
      BREAKING_IS_A_MINOR_BEFORE_1_0,
      PGSTORE_RELEASES_NOTHING,
      REDISREPLAY_RELEASES_NOTHING,
    ],
  },
  redisreplay: {
    dir: "redisreplay",
    releaseRules: [SECURITY_IS_A_PATCH, BREAKING_IS_A_MINOR_BEFORE_1_0],
  },
};

// The directories of modules with no release line yet. Their commits release
// nothing of the root module, and nothing at all until they join MODULES.
const UNRELEASED_DIRS = ["pgstore"];

// tagPrefix is what starts every release tag of the module in dir.
function tagPrefix(dir) {
  return dir === "" ? "" : `${dir}/`;
}

// configFor is the semantic-release config that releases the module name.
function configFor(name) {
  const module = MODULES[name];
  if (!module) {
    throw new Error(`${MODULE_ENV}=${name} names no module; known: ${Object.keys(MODULES).join(", ")}`);
  }
  // Every other module's directory, which the root module's commits exclude.
  const others = [
    ...Object.values(MODULES)
      .map((m) => m.dir)
      .filter((dir) => dir !== "" && dir !== module.dir),
    ...UNRELEASED_DIRS,
  ];
  const analyzer = { preset: PRESET, releaseRules: module.releaseRules };
  const notes = { preset: PRESET };
  const scope = { dir: module.dir, exclude: module.dir === "" ? others : [] };
  return {
    branches: ["main"],
    tagFormat: `${tagPrefix(module.dir)}v\${version}`,
    repositoryUrl: "https://github.com/oleg-tkachuk/limes.git",
    plugins: [
      ["./module-commits.cjs", { ...scope, analyzer, notes }],
      "./release-notes-file.cjs",
      ["./major-guard.cjs", { dir: module.dir, tagPrefix: tagPrefix(module.dir) }],
    ],
    // Read by the tests; semantic-release ignores them.
    analyzer,
    notes,
    scope,
  };
}

module.exports = { MODULES, MODULE_ENV, ROOT_MODULE, UNRELEASED_DIRS, configFor, tagPrefix };
