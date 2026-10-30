import assert from "node:assert/strict";
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, afterEach, before, beforeEach, describe, test } from "node:test";

import { BRANCH, CHANGELOG, GENERATED_MARKER, TITLE, checkChangelog, entryFrom, parseEntries, preamble, proposalTitle, proposeEntries, releaseURL, writeAll, writeChangelog, type Context } from "./changelog.ts";
import { fakeData, fakeGitHub, type FakeData } from "./lib/fake-github.ts";
import { git } from "./lib/git.ts";
import { selectModule } from "./lib/modules.ts";
import { tempRepo, type TempRepo } from "./lib/temp-repo.ts";

const RELEASE = new URL("../release", import.meta.url).pathname;
const REPOSITORY = "example/limes";
const PGSTORE = "pgstore";

// GIT_IDENTITY lets propose commit with no identity of the machine's.
const GIT_IDENTITY: Record<string, string> = {
  GIT_AUTHOR_NAME: "test",
  GIT_AUTHOR_EMAIL: "test@example.com",
  GIT_COMMITTER_NAME: "test",
  GIT_COMMITTER_EMAIL: "test@example.com",
};

// notes is a release's body as GitHub publishes it: semantic-release's notes
// of version with one feature, then GitHub's generated list.
function notes(version: string, feature: string): string {
  return `## [${version}](https://example.com/compare) (2026-10-30)\n\n### Features\n\n* ${feature}\n\n${GENERATED_MARKER} at v${version} -->\n\n## What's Changed\n* a pull request\n`;
}

// headings is the entry headings' versions of a changelog's text.
function headings(text: string): string[] {
  return [...parseEntries(text).keys()];
}

// A checkout with the root module and pgstore, each released.
async function seeded(): Promise<TempRepo> {
  const repo = await tempRepo();
  mkdirSync(join(repo.dir, ".github/release"), { recursive: true });
  copyFileSync(join(RELEASE, "modules.cjs"), join(repo.dir, ".github/release/modules.cjs"));
  repo.write("go.mod", "module example.com/limes\n");
  repo.write(`${PGSTORE}/go.mod`, "module example.com/limes/pgstore\n");
  await repo.commit("init");
  await repo.run("tag", "v0.1.0");
  await repo.run("tag", `${PGSTORE}/v0.1.0`);
  await repo.commit("feat: more");
  await repo.run("tag", "v0.2.0");
  await repo.run("tag", "v0.3.0-rc.1");
  return repo;
}

describe("entryFrom", () => {
  test("keeps the notes above GitHub's generated list", () => {
    assert.equal(entryFrom(notes("0.2.0", "a feature"), "0.2.0", "url"), "## [0.2.0](https://example.com/compare) (2026-10-30)\n\n### Features\n\n* a feature");
  });

  test("gives a release with no notes its heading and a link to it", () => {
    assert.equal(entryFrom(`${GENERATED_MARKER} at pgstore/v0.1.0 -->\n\n## What's Changed\n`, "0.1.0", "url"), "## [0.1.0](url)");
    assert.equal(entryFrom("", "0.1.0", "url"), "## [0.1.0](url)");
  });

  test("moves a top-level heading under the changelog's title", () => {
    assert.match(entryFrom("# [1.0.0](https://example.com/compare)\n\n* a change", "1.0.0", "url"), /^## \[1\.0\.0\]/);
  });
});

describe("parseEntries", () => {
  test("reads each entry by its version, in order", () => {
    const text = `${TITLE}\n\nIntro.\n\n## [0.2.0](a)\n\n* two\n\n## [0.1.0](b)\n\n* one\n`;
    const entries = parseEntries(text);
    assert.deepEqual([...entries.keys()], ["0.2.0", "0.1.0"]);
    assert.equal(entries.get("0.1.0"), "## [0.1.0](b)\n\n* one");
  });

  test("refuses a text with no title, or a version twice", () => {
    assert.throws(() => parseEntries("## [0.1.0](b)\n"), /does not open/);
    assert.throws(() => parseEntries(`${TITLE}\n\n## [0.1.0](a)\n\n## [0.1.0](b)\n`), /0\.1\.0 twice/);
  });
});

describe("the changelogs", () => {
  let repo: TempRepo;
  let data: FakeData;

  beforeEach(async () => {
    repo = await seeded();
    data = fakeData();
    data.notes["v0.1.0"] = notes("0.1.0", "first");
    data.notes["v0.2.0"] = notes("0.2.0", "second");
    data.released.add(`${PGSTORE}/v0.1.0`);
  });
  afterEach(() => repo.cleanup());

  const writer = () => ({ github: fakeGitHub(data), repository: REPOSITORY, root: repo.dir });
  const read = (path: string) => readFileSync(join(repo.dir, path), "utf8");

  test("writes every module's releases, newest first, prereleases left out", async () => {
    assert.deepEqual(await writeAll(writer(), "HEAD"), ["v0.2.0", "v0.1.0", `${PGSTORE}/v0.1.0`]);
    const root = read(CHANGELOG);
    assert.ok(root.startsWith(preamble(selectModule(repo.dir))));
    assert.deepEqual(headings(root), ["0.2.0", "0.1.0"]);
    assert.match(root, /\* second\n\n## \[0\.1\.0\]/);
    assert.doesNotMatch(root, /What's Changed/);
    assert.equal(parseEntries(read(join(PGSTORE, CHANGELOG))).get("0.1.0"), `## [0.1.0](${releaseURL(REPOSITORY, `${PGSTORE}/v0.1.0`)})`);
  });

  test("adds only the missing entries, and leaves the written ones as they are", async () => {
    await writeAll(writer(), "HEAD");
    repo.write(CHANGELOG, read(CHANGELOG).replace("* first", "* first, as written"));
    await repo.commit("feat: third");
    await repo.run("tag", "v0.3.0");
    data.notes["v0.3.0"] = notes("0.3.0", "third");
    data.calls.length = 0;
    assert.deepEqual(await writeChangelog(writer(), selectModule(repo.dir), "HEAD"), ["v0.3.0"]);
    assert.deepEqual(data.calls, ["releaseNotes v0.3.0"]);
    assert.deepEqual(headings(read(CHANGELOG)), ["0.3.0", "0.2.0", "0.1.0"]);
    assert.match(read(CHANGELOG), /\* first, as written/);
    assert.deepEqual(await writeAll(writer(), "HEAD"), []);
  });

  test("waits for a release GitHub has not published", async () => {
    delete data.notes["v0.2.0"];
    assert.deepEqual(await writeChangelog(writer(), selectModule(repo.dir), "HEAD"), ["v0.1.0"]);
    assert.deepEqual(headings(read(CHANGELOG)), ["0.1.0"]);
  });

  test("check passes entries that are tags, newest first, some still missing", async () => {
    await writeAll(writer(), "HEAD");
    await repo.commit("feat: third");
    await repo.run("tag", "v0.3.0");
    assert.match(await checkChangelog(repo.dir, selectModule(repo.dir), "HEAD"), /lists 2 releases.*1 still to be written/);
  });

  test("check passes a module with no changelog yet", async () => {
    assert.match(await checkChangelog(repo.dir, selectModule(repo.dir, PGSTORE), "HEAD"), /lists 0 releases/);
  });

  test("check refuses an entry that is no tag, or out of order", async () => {
    repo.write(CHANGELOG, `${TITLE}\n\n## [0.9.0](a)\n`);
    await assert.rejects(checkChangelog(repo.dir, selectModule(repo.dir), "HEAD"), /lists 0\.9\.0, no release tag v0\.9\.0 is reachable/);
    repo.write(CHANGELOG, `${TITLE}\n\n## [0.1.0](a)\n\n## [0.2.0](b)\n`);
    await assert.rejects(checkChangelog(repo.dir, selectModule(repo.dir), "HEAD"), /lists 0\.2\.0, out of order/);
  });
});

describe("proposalTitle", () => {
  test("names a few tags, and counts more", () => {
    assert.equal(proposalTitle(["v0.2.0", "pgstore/v0.3.1"]), "docs(changelog): add v0.2.0, pgstore/v0.3.1");
    assert.equal(proposalTitle(["v4", "v3", "v2", "v1"]), "docs(changelog): add 4 releases");
  });
});

describe("proposeEntries", () => {
  let work: string;
  let repo: TempRepo;
  let data: FakeData;
  const saved: Record<string, string | undefined> = {};

  before(() => {
    for (const [k, v] of Object.entries(GIT_IDENTITY)) {
      saved[k] = process.env[k];
      process.env[k] = v;
    }
  });
  after(() => {
    for (const [k, v] of Object.entries(saved)) {
      if (v === undefined) delete process.env[k];
      else process.env[k] = v;
    }
  });

  // A checkout of main whose origin is a bare repository beside it, with a
  // release tagged on the remote alone.
  beforeEach(async () => {
    work = mkdtempSync(join(tmpdir(), "limes-changelog-"));
    const origin = join(work, "origin.git");
    await git(["init", "--quiet", "--bare", "--initial-branch=main", origin]);
    repo = await tempRepo();
    await repo.run("remote", "add", "origin", origin);
    mkdirSync(join(repo.dir, ".github/release"), { recursive: true });
    copyFileSync(join(RELEASE, "modules.cjs"), join(repo.dir, ".github/release/modules.cjs"));
    repo.write("go.mod", "module example.com/limes\n");
    await repo.commit("init");
    await repo.run("tag", "v0.1.0");
    await repo.run("push", "--quiet", "origin", "main", "v0.1.0");
    await repo.run("tag", "--delete", "v0.1.0");
    data = fakeData();
    data.notes["v0.1.0"] = notes("0.1.0", "first");
  });
  afterEach(() => {
    repo.cleanup();
    rmSync(work, { recursive: true, force: true });
  });

  function ctx(): Context {
    return {
      github: fakeGitHub(data),
      repository: REPOSITORY,
      root: repo.dir,
      push: async (branch) => void (await repo.run("push", "--quiet", "--force", "origin", `HEAD:refs/heads/${branch}`)),
    };
  }

  test("commits the new entries on its branch and opens a pull request", async () => {
    assert.equal(await proposeEntries(ctx()), 101);
    assert.deepEqual(data.createdPulls.map((p) => [p.head, p.base, p.text.title]), [[BRANCH, "main", proposalTitle(["v0.1.0"])]]);
    assert.equal((await repo.run("log", "-1", "--format=%s", `origin/${BRANCH}`)).trim(), proposalTitle(["v0.1.0"]));
    assert.deepEqual(headings(await repo.run("show", `origin/${BRANCH}:${CHANGELOG}`)), ["0.1.0"]);
  });

  test("updates the pull request still open", async () => {
    data.pullsFrom.set(BRANCH, 7);
    assert.equal(await proposeEntries(ctx()), 7);
    assert.deepEqual(data.createdPulls, []);
    assert.deepEqual(data.editedPulls.map((p) => p.number), [7]);
  });

  test("proposes nothing when main's changelogs lack no entry", async () => {
    await repo.run("fetch", "--quiet", "--tags", "origin");
    await writeAll(ctx(), "HEAD");
    await repo.commit("docs: the changelog");
    await repo.run("push", "--quiet", "origin", "main");
    assert.equal(await proposeEntries(ctx()), undefined);
    assert.deepEqual(data.createdPulls, []);
  });
});
