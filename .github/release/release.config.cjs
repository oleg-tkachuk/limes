// Computes the next SemVer from Conventional Commits, writes the release notes
// and creates the git tag — nothing else. Run by .github/workflows/release.yaml, which ci.yaml dispatches
// once every check on a push to main has passed.
//
// The tag IS the release: the Go proxy reads it. The GitHub release beside it
// is notes for people, created by the workflow with `gh release create`: the
// notes generated here from the commits — breaking changes first — and below
// them GitHub's list of merged pull requests, shaped by .github/release.yml.
// So no @semantic-release/github here; release-notes-file.cjs hands the notes
// over. release-notes.test.mjs pins them.
//
// `conventionalcommits` preset: feat is a minor, fix and perf are a patch,
// and docs/style/refactor/test/build/ci/chore release nothing. `security` is a
// patch: the preset knows no such type. The module is pre-1.0, where a
// breaking change is a minor; that rule goes when it reaches 1.0.
// release-rules.test.mjs pins all of it.

const PRESET = "conventionalcommits";
const SECURITY_IS_A_PATCH = { type: "security", release: "patch" };
const BREAKING_IS_A_MINOR_BEFORE_1_0 = { breaking: true, release: "minor" };

const analyzer = {
  preset: PRESET,
  releaseRules: [SECURITY_IS_A_PATCH, BREAKING_IS_A_MINOR_BEFORE_1_0],
};

const notes = { preset: PRESET };

module.exports = {
  branches: ["main"],
  tagFormat: "v${version}",
  repositoryUrl: "https://github.com/oleg-tkachuk/limes.git",
  plugins: [
    ["@semantic-release/commit-analyzer", analyzer],
    ["@semantic-release/release-notes-generator", notes],
    "./release-notes-file.cjs",
  ],
  // Read by release-rules.test.mjs and release-notes.test.mjs;
  // semantic-release ignores them.
  analyzer,
  notes,
};
