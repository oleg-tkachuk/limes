// A semantic-release plugin that releases one module of the repository: it
// hands the commit analyzer and the notes generator only the commits that
// touch that module, and delegates to them.
//
// It replaces both in the plugin list rather than sitting beside them:
// semantic-release takes the highest release any analyzeCommits returns, so
// an analyzer that saw every commit would still cut a release for a change
// to another module.
//
// A commit touches the module in dir when a path it changes lies under dir;
// the root module (dir empty) when a path lies outside every directory in
// exclude, the other modules. Paths are read against the first parent, so a
// merge commit — whose body is the pull request's title, which the analyzer
// reads — counts for the modules its pull request changed.

const { execFileSync } = require("node:child_process");

// git diff-tree arguments that list a commit's paths against its first
// parent, NUL-separated: -m --first-parent so a merge lists what it brought
// in, --root so the first commit lists everything.
const DIFF_TREE_ARGS = ["diff-tree", "--no-commit-id", "--name-only", "-r", "--root", "-m", "--first-parent", "-z"];
const PATH_SEP = "\0";

// changedPaths lists the paths commit hash changes against its first parent.
function changedPaths(cwd, hash) {
  const out = execFileSync("git", [...DIFF_TREE_ARGS, hash], { cwd, encoding: "utf8" });
  return out.split(PATH_SEP).filter((p) => p !== "");
}

// under reports whether path lies in directory dir.
function under(path, dir) {
  return path.startsWith(`${dir}/`);
}

// touches reports whether a commit changing paths belongs to the module the
// scope names.
function touches(paths, { dir, exclude = [] }) {
  if (dir !== "") {
    return paths.some((p) => under(p, dir));
  }
  return paths.some((p) => !exclude.some((other) => under(p, other)));
}

// moduleCommits is the context with its commits narrowed to the module's.
function moduleCommits(scope, context) {
  const commits = context.commits.filter((c) => touches(changedPaths(context.cwd, c.hash), scope));
  context.logger.log(`${commits.length} of ${context.commits.length} commits touch the module in ${scope.dir || "the root"}`);
  return { ...context, commits };
}

async function analyzeCommits(pluginConfig, context) {
  const { analyzeCommits: analyze } = await import("@semantic-release/commit-analyzer");
  return analyze(pluginConfig.analyzer, moduleCommits(pluginConfig, context));
}

async function generateNotes(pluginConfig, context) {
  const { generateNotes: generate } = await import("@semantic-release/release-notes-generator");
  return generate(pluginConfig.notes, moduleCommits(pluginConfig, context));
}

module.exports = { analyzeCommits, generateNotes, changedPaths, touches };

// Run as a command, it reads commit hashes from stdin, one a line, and prints
// those that touch the module named, as modules.cjs at the path given scopes
// it — so the checks of breaking markers count the commits the release does.
//
//   node module-commits.cjs <modules.cjs> <module> <hashes
if (require.main === module) {
  const [modulesPath, name] = process.argv.slice(2);
  const { configFor } = require(require("node:path").resolve(modulesPath));
  const scope = configFor(name).plugins.find((p) => Array.isArray(p) && p[0] === "./module-commits.cjs")[1];
  const hashes = require("node:fs").readFileSync(0, "utf8").split("\n").filter((h) => h !== "");
  for (const hash of hashes) {
    if (touches(changedPaths(process.cwd(), hash), scope)) {
      console.log(hash);
    }
  }
}
