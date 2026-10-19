import assert from "node:assert/strict";
import { copyFileSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { after, before, describe, test } from "node:test";

import { git } from "./lib/git.ts";
import { releaseCommits, selectModule, type ReleaseCommits } from "./lib/modules.ts";
import { tempRepo, type TempRepo } from "./lib/temp-repo.ts";
import { apiBreakCheck, apidiffCommand, checkRange, markerCheck, withoutCommands } from "./release-checks.ts";

// RELEASE is the release tooling beside these scripts, which the scratch
// repositories copy their module table and config from.
const RELEASE = new URL("../release/", import.meta.url).pathname;
// REPO_ROOT is this checkout, whose tools/go.mod pins apidiff.
const REPO_ROOT = new URL("../../", import.meta.url).pathname;
// NESTED is the nested module the two-module cases use.
const NESTED = "redisreplay";
// PRE_STABLE_LINE is the pre-1.0 rule as the root's rules list it, a rule a
// line.
const PRE_STABLE_LINE = "      BREAKING_IS_A_MINOR_BEFORE_1_0,";
const ROOT_FIRST_RULE = "      SECURITY_IS_A_PATCH,";

const commits = releaseCommits();

// copyRelease puts the module table, and with it the release config, into
// the repository at dir.
function copyRelease(dir: string, files: string[] = ["modules.cjs", "release.config.cjs"]): void {
  mkdirSync(join(dir, ".github/release"), { recursive: true });
  for (const f of files) {
    copyFileSync(join(RELEASE, f), join(dir, ".github/release", f));
  }
}

// withPreStableRoot gives the copied table the root module's pre-1.0 rule
// back if it has none: these cases are about where a marker counts for a
// module before 1.0.
function withPreStableRoot(dir: string): void {
  const path = join(dir, ".github/release/modules.cjs");
  const table = readFileSync(path, "utf8");
  if (!table.includes(PRE_STABLE_LINE)) {
    writeFileSync(path, table.replace(`${ROOT_FIRST_RULE}\n`, `${ROOT_FIRST_RULE}\n${PRE_STABLE_LINE}\n`));
  }
}

describe("checkRange", () => {
  let repo: TempRepo;
  before(async () => {
    repo = await tempRepo();
  });
  after(() => repo.cleanup());

  test("before any release, a push has nothing to check from", async () => {
    const first = await repo.commit("feat: start");
    assert.deepEqual(await checkRange(selectModule(repo.dir), "push", "", "", first, repo.dir), { base: undefined, head: first });
    await repo.run("tag", "v0.1.0");
    await repo.run("tag", "v0.2.0");
    const later = await repo.commit("fix: later");
    assert.deepEqual(await checkRange(selectModule(repo.dir), "push", "", "", later, repo.dir), { base: "v0.2.0", head: later });
    assert.deepEqual(await checkRange(selectModule(repo.dir), "pull_request", first, later, later, repo.dir), { base: first, head: later });

    // With the module table, a push is checked from the module's own tag.
    copyRelease(repo.dir, ["modules.cjs"]);
    await repo.commit("build: add the module table");
    await repo.run("tag", `${NESTED}/v0.1.0`);
    const nestedLater = await repo.commit(`fix(${NESTED}): later`);
    assert.deepEqual(await checkRange(selectModule(repo.dir), "push", "", "", nestedLater, repo.dir), { base: "v0.2.0", head: nestedLater });
    assert.deepEqual(await checkRange(selectModule(repo.dir, NESTED), "push", "", "", nestedLater, repo.dir), {
      base: `${NESTED}/v0.1.0`,
      head: nestedLater,
    });
    assert.deepEqual(await checkRange(selectModule(repo.dir, NESTED), "pull_request", first, nestedLater, nestedLater, repo.dir), {
      base: first,
      head: nestedLater,
    });
  });
});

describe("markerCheck", () => {
  let repo: TempRepo;
  before(async () => {
    repo = await tempRepo();
    await repo.commit("feat: start");
  });
  after(() => repo.cleanup());

  // check commits messages on a branch off main — each with a migration
  // entry when migration is set — and runs the check of main..case.
  async function check(messages: string[], opts: { migration?: boolean; title?: string; with?: ReleaseCommits } = {}): Promise<boolean> {
    await repo.run("checkout", "--quiet", "-B", "case", "main");
    for (const message of messages) {
      const readme = join(repo.dir, "README.md");
      writeFileSync(readme, `${readFileSafe(readme)}${message}\n`);
      if (opts.migration === true) {
        const guide = join(repo.dir, "docs/migration.md");
        mkdirSync(join(repo.dir, "docs"), { recursive: true });
        writeFileSync(guide, `${readFileSafe(guide)}${message}\n`);
      }
      await repo.commit(message);
    }
    const verdict = await markerCheck(selectModule(repo.dir), "main", "case", opts.title, opts.with ?? commits, repo.dir);
    await repo.run("checkout", "--quiet", "main");
    return verdict.ok;
  }

  test("before 1.0, with and without a migration entry", async () => {
    // Before any release, and on 0.x, a marker cuts a minor: it passes when
    // the change writes its migration entry.
    assert.equal(await check(["feat!: change"], { migration: true }), true, "no release, a marker");
    await repo.run("tag", "v0.9.0", "main");
    assert.equal(await check(["feat!: change"], { migration: true }), true, "on v0, a marker");
    assert.equal(await check(["fix: x\n\nBREAKING CHANGE: gone"], { migration: true }), true, "on v0, a footer");
    assert.equal(await check(["feat!: change"]), false, "on v0, a marker without a migration entry");
    assert.equal(await check(["fix: x\n\nBREAKING CHANGE: gone"]), false, "on v0, a footer without a migration entry");
    assert.equal(await check(["feat!: change", "docs: more"], { migration: true }), true, "on v0, a marker in an earlier commit");
    assert.equal(await check(["docs: explain"]), true, "on v0, no marker");
  });

  test("a title that reads as a footer is refused on any line", async () => {
    assert.equal(await check(["docs: explain"], { title: "Breaking change: drop X" }), false);
    assert.equal(await check(["docs: explain"], { title: "* BREAKING CHANGE: drop X" }), false);
    assert.equal(await check(["docs: explain"], { title: "docs: explain the breaking change" }), true);
  });

  test("the root module's own rules decide, from the module table", async () => {
    copyRelease(repo.dir);
    withPreStableRoot(repo.dir);
    await repo.commit("build: release from the module table");
    assert.equal(await check(["feat!: change"], { migration: true }), true, "the pre-1.0 rule in the module table");
    const table = join(repo.dir, ".github/release/modules.cjs");
    writeFileSync(table, readFileSync(table, "utf8").replace(`${PRE_STABLE_LINE}\n`, ""));
    await repo.commit("build!: release root breaks as majors");
    assert.equal(await check(["feat!: change"]), false, "the root's pre-1.0 rule gone from the module table");
    await repo.run("rm", "--quiet", "-r", ".github");
    await repo.commit("build: drop the module table");
  });

  test("the release config decides before the tag does", async () => {
    const config = join(repo.dir, ".github/release/release.config.cjs");
    mkdirSync(join(repo.dir, ".github/release"), { recursive: true });
    writeFileSync(config, "const BREAKING_IS_A_MINOR_BEFORE_1_0 = {};\n");
    await repo.commit("build: release breaks as minors");
    assert.equal(await check(["feat!: change"], { migration: true }), true, "the pre-1.0 rule in force");
    writeFileSync(config, "module.exports = {};\n");
    await repo.commit("build!: release breaks as majors");
    assert.equal(await check(["feat!: change"]), false, "the rule gone, v1.0.0 not tagged yet");
    assert.equal(await check(["feat: add"]), true, "the rule gone, no marker");
  });

  test("from v1.0.0 on, every marker the parser reads is refused", async () => {
    await repo.run("tag", "v1.0.0", "main");
    const cases: [string, string[], boolean][] = [
      ["no marker", ["docs: explain", "feat: add"], true],
      ["a subject marker", ["feat!: change"], false],
      ["a scoped marker", ["fix(api)!: change"], false],
      ["a capitalised type", ["Docs!: change"], false],
      ["a footer", ["docs: explain\n\nBREAKING CHANGE: gone"], false],
      ["a footer in another case", ["docs: explain\n\nbreaking change: gone"], false],
      ["a marker in an earlier commit", ["docs!: explain", "docs: more"], false],
      ["a ! outside the type", ["fix: a!: b"], true],
    ];
    for (const [name, messages, want] of cases) {
      assert.equal(await check(messages), want, name);
    }
  });
});

// readFileSafe is a file's content, or nothing when it does not exist.
function readFileSafe(path: string): string {
  try {
    return readFileSync(path, "utf8");
  } catch {
    return "";
  }
}

describe("markerCheck over two modules", () => {
  let repo: TempRepo;
  before(async () => {
    repo = await tempRepo();
    copyRelease(repo.dir);
    withPreStableRoot(repo.dir);
    repo.write("README.md", "");
    repo.write(`${NESTED}/README.md`, "");
    await repo.commit("feat: start");
    await repo.run("tag", "v0.1.0");
    await repo.run("tag", `${NESTED}/v0.1.0`);
  });
  after(() => repo.cleanup());

  // checkModule commits message touching path on a branch off main and runs
  // the check of main..case for module.
  async function checkModule(module: string, path: string, message: string, opts: { migration?: boolean; with?: ReleaseCommits } = {}) {
    await repo.run("checkout", "--quiet", "-B", "case", "main");
    writeFileSync(join(repo.dir, path), `${readFileSafe(join(repo.dir, path))}${message}\n`);
    if (opts.migration === true) {
      mkdirSync(join(repo.dir, "docs"), { recursive: true });
      writeFileSync(join(repo.dir, "docs/migration.md"), `${message}\n`);
    }
    await repo.commit(message);
    try {
      return (await markerCheck(selectModule(repo.dir, module), "main", "case", undefined, opts.with ?? commits, repo.dir)).ok;
    } finally {
      await repo.run("checkout", "--quiet", "main");
    }
  }

  test("a marker counts for the module its commit touches", async () => {
    assert.equal(await checkModule(NESTED, `${NESTED}/README.md`, `feat(${NESTED})!: change`), false, "a nested marker with no migration entry");
    assert.equal(await checkModule("limes", `${NESTED}/README.md`, `feat(${NESTED})!: change`), true, "a nested marker, checked for the root");
    assert.equal(await checkModule(NESTED, "README.md", "feat!: change"), true, "a root marker, checked for the nested module");
    assert.equal(
      await checkModule(NESTED, `${NESTED}/README.md`, `feat(${NESTED})!: change`, { migration: true }),
      true,
      "a nested marker with its migration entry",
    );
    await repo.run("tag", `${NESTED}/v1.0.0`, "main");
    assert.equal(await checkModule(NESTED, `${NESTED}/README.md`, `feat(${NESTED})!: change`), false, "a nested marker on its stable line");
    assert.equal(await checkModule("limes", "README.md", "feat!: change", { migration: true }), true, "a root marker beside the nested stable line");
  });

  // The release analyzer failing to run is no range without a marker: the
  // check fails rather than passing a marker it never read.
  test("an analyzer that cannot run fails the check", async () => {
    const broken: ReleaseCommits = {
      ...commits,
      releases: async () => {
        throw new Error("the analyzer did not load");
      },
    };
    await assert.rejects(checkModule("limes", "README.md", "feat!: change", { with: broken }));
  });

  test("a module the table does not hold is refused", () => {
    assert.throws(() => selectModule(repo.dir, "no-such-module"));
  });
});

describe("withoutCommands", () => {
  test("drops a command's removal and its members", () => {
    const breaks = ["- package example.com/m/cmd: removed", "- ./cmd.Exported: removed", "- A: removed"];
    assert.deepEqual(withoutCommands(breaks, "example.com/m", ["example.com/m/cmd"]), ["- A: removed"]);
  });
});

describe("apiBreakCheck", () => {
  let repo: TempRepo;
  const apidiff = apidiffCommand(REPO_ROOT, undefined);

  before(async () => {
    repo = await tempRepo();
    repo.write("go.mod", "module example.com/m\n\ngo 1.26\n");
    repo.write("m.go", "package m\n\nfunc A() {}\n");
    repo.write("cmd/main.go", "package main\n\nfunc Exported() {}\n\nfunc main() {}\n");
    await repo.commit("feat: add A");
  });
  after(() => repo.cleanup());

  type Change = "drop" | "add" | "dropcmd" | "cmdexport" | "dropboth";
  async function change(kind: Change): Promise<void> {
    switch (kind) {
      case "drop":
        repo.write("m.go", "package m\n");
        break;
      case "add":
        repo.write("m.go", "package m\n\nfunc A() {}\n\nfunc B() {}\n");
        break;
      case "dropcmd":
        await repo.run("rm", "--quiet", "-r", "cmd");
        break;
      case "cmdexport":
        repo.write("cmd/main.go", "package main\n\nfunc main() {}\n");
        break;
      case "dropboth":
        repo.write("m.go", "package m\n");
        await repo.run("rm", "--quiet", "-r", "cmd");
        break;
    }
  }

  // check makes kind of change on a branch off main, committed as messages,
  // and runs the check of main..case.
  async function check(kind: Change, ...messages: string[]): Promise<boolean> {
    await repo.run("checkout", "--quiet", "-B", "case", "main");
    await change(kind);
    for (const message of messages) {
      await repo.commit(message);
    }
    try {
      return (await apiBreakCheck(selectModule(repo.dir), "main", "case", apidiff, commits, repo.dir)).ok;
    } finally {
      await repo.run("checkout", "--quiet", "--force", "main");
    }
  }

  const cases: [string, boolean, Change, string[]][] = [
    ["an unmarked break", false, "drop", ["fix: drop A"]],
    ["a break marked in the subject", true, "drop", ["fix!: drop A"]],
    ["a break marked with a scope", true, "drop", ["refactor(m)!: drop A"]],
    ["a break marked in a footer", true, "drop", ["fix: drop A\n\nBREAKING CHANGE: A is gone"]],
    ["a break marked by a later commit", true, "drop", ["refactor: drop A", "docs!: say A is gone"]],
    ["a break marked by an earlier commit", true, "drop", ["fix!: drop A", "docs: say A is gone"]],
    ["a ! that is not in the type", false, "drop", ["fix: drop A!: really"]],
    ["a break marked in a hyphenated footer", true, "drop", ["fix: drop A\n\nBREAKING-CHANGE: A is gone"]],
    ["a marker in the body, not the subject", false, "drop", ["fix: drop A\n\nfix!: is not a subject"]],
    ["a capitalised type", true, "drop", ["Refactor!: drop A"]],
    ["a type with an underscore", true, "drop", ["feat_x!: drop A"]],
    ["a footer in another case", true, "drop", ["fix: drop A\n\nBreaking change: A is gone"]],
    ["a footer in a list", true, "drop", ["fix: drop A\n\n* BREAKING CHANGE: A is gone"]],
    ["a footer with no colon", true, "drop", ["fix: drop A\n\nBREAKING-CHANGE A is gone"]],
    ["a scope with a parenthesis in it", true, "drop", ["feat(a)b)!: drop A"]],
    ["two scopes", true, "drop", ["feat(a)(b)!: drop A"]],
    ["a type that is not ASCII", false, "drop", ["féat!: drop A"]],
    ["a footer as the subject", false, "drop", ["BREAKING CHANGE: drop A"]],
    ["a compatible addition", true, "add", ["feat: add B"]],
    ["a marked change with no break", true, "add", ["feat!: change the wire format"]],
    ["a command removed", true, "dropcmd", ["docs: move the command"]],
    ["a command's exported function removed", true, "cmdexport", ["docs: trim the command"]],
    ["a command removed beside an unmarked break", false, "dropboth", ["docs: drop A and the command"]],
  ];
  for (const [name, want, kind, messages] of cases) {
    test(name, async () => assert.equal(await check(kind, ...messages), want));
  }

  // From v1.0.0 on, no break passes, marked or not.
  test("on v1", async () => {
    await repo.run("tag", "v1.0.0", "main");
    assert.equal(await check("drop", "fix: drop A"), false, "an unmarked break");
    assert.equal(await check("drop", "fix!: drop A"), false, "a marked break");
    assert.equal(await check("add", "feat: add B"), true, "a compatible addition");
    await repo.run("tag", "-d", "v1.0.0");
  });

  test("leaves no worktree behind", async () => {
    assert.equal((await git(["worktree", "list"], repo.dir)).trim().split("\n").length, 1);
  });
});

describe("apiBreakCheck over two modules", () => {
  let repo: TempRepo;
  const apidiff = apidiffCommand(REPO_ROOT, undefined);

  before(async () => {
    repo = await tempRepo();
    copyRelease(repo.dir, ["modules.cjs"]);
    withPreStableRoot(repo.dir);
    repo.write("go.mod", "module example.com/m\n\ngo 1.26\n");
    repo.write("m.go", "package m\n\nfunc A() {}\n");
    await repo.commit("feat: add A");
    await repo.run("checkout", "--quiet", "-b", "without-nested");
    repo.write(`${NESTED}/go.mod`, `module example.com/m/${NESTED}\n\ngo 1.26\n`);
    repo.write(`${NESTED}/r.go`, `package ${NESTED}\n\nfunc R() {}\n`);
    await repo.commit(`feat(${NESTED}): add R`);
    await repo.run("checkout", "--quiet", "main");
    await repo.run("merge", "--quiet", "--ff-only", "without-nested");
  });
  after(() => repo.cleanup());

  async function verdictOf(base: string, module: string): Promise<boolean> {
    try {
      return (await apiBreakCheck(selectModule(repo.dir, module), base, "case", apidiff, commits, repo.dir)).ok;
    } finally {
      await repo.run("checkout", "--quiet", "--force", "main");
    }
  }

  // checkModule drops R in a first commit; each message after it is a
  // commit touching the root module only.
  async function checkModule(module: string, first: string, ...rest: string[]): Promise<boolean> {
    await repo.run("checkout", "--quiet", "-B", "case", "main");
    repo.write(`${NESTED}/r.go`, `package ${NESTED}\n`);
    await repo.commit(first);
    for (const message of rest) {
      repo.write("m.go", `${readFileSync(join(repo.dir, "m.go"), "utf8")}// ${message}\n`);
      await repo.commit(message);
    }
    return verdictOf("main", module);
  }

  test("a nested module's break, by its own markers", async () => {
    assert.equal(await checkModule(NESTED, `fix(${NESTED}): drop R`), false, "unmarked");
    assert.equal(await checkModule(NESTED, `fix(${NESTED})!: drop R`), true, "marked on its commit");
    assert.equal(await checkModule(NESTED, `fix(${NESTED}): drop R`, "docs!: say R is gone"), false, "marked on a root commit only");
    assert.equal(await checkModule("limes", `fix(${NESTED}): drop R`), true, "checked for the root");
  });

  // One commit moves A out of the root module into the nested one, so it
  // touches both: the root's rules release nothing of a commit scoped to a
  // nested module, so its marker is none of the root's.
  test("a root break in a commit scoped to the nested module", async () => {
    async function checkRoot(message: string): Promise<boolean> {
      await repo.run("checkout", "--quiet", "-B", "case", "main");
      repo.write("m.go", "package m\n");
      repo.write(`${NESTED}/r.go`, `package ${NESTED}\n\nfunc R() {}\n\nfunc A() {}\n`);
      await repo.commit(message);
      return verdictOf("main", "limes");
    }
    assert.equal(await checkRoot(`refactor!: move A to ${NESTED}`), true, "marked for the root");
    assert.equal(await checkRoot(`refactor(${NESTED})!: move A to ${NESTED}`), false, "marked only under the nested scope");
    assert.equal(await checkRoot(`refactor(${NESTED}): move A\n\nBREAKING CHANGE: A moved`), false, "a nested-scoped footer");
  });

  test("a module new since the merge base has no API to break", async () => {
    await repo.run("checkout", "--quiet", "-B", "case", "without-nested");
    await repo.run("branch", "--force", "base", "main~1");
    assert.equal(await verdictOf("base", NESTED), true);
  });
});
