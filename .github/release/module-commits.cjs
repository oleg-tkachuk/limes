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
// merge commit counts for the modules its pull request changed; it cuts no
// release itself, the preset finding no type in a merge's subject, and the
// commits it brought are counted each on its own.

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

// generateNotes lists the module's commits that its own analyzer releases: a
// commit scoped to another module, which the module's rules release nothing
// of, stays out of its notes even when it touches the module's paths, and so
// does its breaking marker.
async function generateNotes(pluginConfig, context) {
  const { generateNotes: generate } = await import("@semantic-release/release-notes-generator");
  const narrowed = moduleCommits(pluginConfig, context);
  const released = [];
  for (const c of narrowed.commits) {
    if (await releases(pluginConfig.analyzer, c.hash, c.message)) {
      released.push(c);
    }
  }
  context.logger.log(`${released.length} of them release the module in ${pluginConfig.dir || "the root"}, and are noted`);
  return generate(pluginConfig.notes, { ...narrowed, commits: released });
}

module.exports = { analyzeCommits, generateNotes, changedPaths, touches };

// releases reports whether the module's own analyzer cuts a release for the
// commit: a scope its rules release nothing of outranks a breaking marker,
// so such a marker says nothing of this module.
async function releases(analyzer, hash, message) {
  return (await releaseType(analyzer, hash, message)) != null;
}

// releaseType is the release the module's own analyzer cuts for the commit —
// major, minor or patch — or null for none.
async function releaseType(analyzer, hash, message) {
  const { analyzeCommits: analyze } = await import("@semantic-release/commit-analyzer");
  const logger = { log() {}, error() {}, warn() {}, success() {} };
  return (await analyze(analyzer, { commits: [{ hash, message }], logger, cwd: process.cwd(), options: {} })) ?? null;
}

module.exports.releases = releases;
module.exports.releaseType = releaseType;

// RELEASED_FLAG narrows the command's output to the commits the module's
// analyzer releases.
const RELEASED_FLAG = "--released";

// Run as a command, it reads commit hashes from stdin, one a line, and prints
// those that touch the module named, as modules.cjs at the path given scopes
// it — so the checks of breaking markers count the commits the release does.
// With --released, only those the module's analyzer cuts a release for.
//
//   node module-commits.cjs <modules.cjs> <module> [--released] <hashes
if (require.main === module) {
  (async () => {
    const [modulesPath, name, flag] = process.argv.slice(2);
    const { configFor } = require(require("node:path").resolve(modulesPath));
    const cfg = configFor(name);
    const scope = cfg.plugins.find((p) => Array.isArray(p) && p[0] === "./module-commits.cjs")[1];
    const hashes = require("node:fs").readFileSync(0, "utf8").split("\n").filter((h) => h !== "");
    for (const hash of hashes) {
      if (!touches(changedPaths(process.cwd(), hash), scope)) {
        continue;
      }
      if (flag === RELEASED_FLAG) {
        const message = execFileSync("git", ["log", "-1", "--format=%B", hash], { cwd: process.cwd(), encoding: "utf8" });
        if (!(await releases(cfg.analyzer, hash, message))) {
          continue;
        }
      }
      console.log(hash);
    }
  })().catch((err) => {
    console.error(err);
    process.exit(1);
  });
}
