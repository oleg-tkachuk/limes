// Computes the next SemVer from Conventional Commits and creates the git tag —
// nothing else. Run by .github/workflows/release.yaml, which ci.yaml dispatches
// once every check on a push to main has passed.
//
// The tag IS the release: the Go proxy reads it. The GitHub release beside it
// is notes for people, created by the workflow with
// `gh release create --generate-notes` and shaped by .github/release.yml — so
// no @semantic-release/github here.
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

module.exports = {
  branches: ["main"],
  tagFormat: "v${version}",
  repositoryUrl: "https://github.com/oleg-tkachuk/limes.git",
  plugins: [["@semantic-release/commit-analyzer", analyzer]],
  // Read by release-rules.test.mjs; semantic-release ignores it.
  analyzer,
};
