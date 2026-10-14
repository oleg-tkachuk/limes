// The modules this repository releases, and the semantic-release config of
// each; release.config.cjs picks one.
//
// It requires nothing: the CI checks evaluate a copy of it from an earlier
// commit, on its own, to learn which rules a module released by there
// (.github/scripts/breaking-markers.sh).
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
// patch: the preset knows no such type. A nested module's own build change is
// a patch of it: such a commit changes the go.mod its users resolve — above
// all the release workflow's `build(<module>): require limes vX.Y.Z`, which
// would otherwise leave the requirement unreleased. Every module is pre-1.0, where a
// breaking change is a minor; that rule goes from a module's own rules when
// it reaches 1.0. release-rules.test.mjs pins all of it. major-guard.cjs
// refuses a major the module path does not carry, whatever marker cut it.

const PRESET = "conventionalcommits";
const SECURITY_IS_A_PATCH = { type: "security", release: "patch" };
const BREAKING_IS_A_MINOR_BEFORE_1_0 = { breaking: true, release: "minor" };
// The nested modules: each one's name, directory and commit scope.
const PGSTORE_MODULE = "pgstore";
const REDISREPLAY_MODULE = "redisreplay";
// A pgstore or redisreplay commit cuts no release of the limes module. Their
// directories are left out of its commits already, each module being on a
// line of its own; these cover a commit scoped to one that also touches a
// path outside it. A rule releasing nothing outranks the breaking rule
// above, so a breaking change scoped to one cuts no limes release either.
const PGSTORE_RELEASES_NOTHING = { scope: PGSTORE_MODULE, release: false };
const REDISREPLAY_RELEASES_NOTHING = { scope: REDISREPLAY_MODULE, release: false };

// buildIsAPatch is the rule that releases a build change scoped to the nested
// module name as a patch of it.
function buildIsAPatch(name) {
  return { type: "build", scope: name, release: "patch" };
}

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
  [REDISREPLAY_MODULE]: {
    dir: REDISREPLAY_MODULE,
    releaseRules: [SECURITY_IS_A_PATCH, BREAKING_IS_A_MINOR_BEFORE_1_0, buildIsAPatch(REDISREPLAY_MODULE)],
  },
  [PGSTORE_MODULE]: {
    dir: PGSTORE_MODULE,
    releaseRules: [SECURITY_IS_A_PATCH, BREAKING_IS_A_MINOR_BEFORE_1_0, buildIsAPatch(PGSTORE_MODULE)],
  },
};

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
  const others = Object.values(MODULES)
    .map((m) => m.dir)
    .filter((dir) => dir !== "" && dir !== module.dir);
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

// breaksAreMinor reports whether the module name still releases a breaking
// change as a minor, as every module does before 1.0. The checks of breaking
// markers and of the API ask it rather than read the rules themselves.
function breaksAreMinor(name) {
  return configFor(name).analyzer.releaseRules.includes(BREAKING_IS_A_MINOR_BEFORE_1_0);
}

module.exports = {
  MODULES,
  MODULE_ENV,
  ROOT_MODULE,
  PGSTORE_MODULE,
  REDISREPLAY_MODULE,
  breaksAreMinor,
  configFor,
  tagPrefix,
};
