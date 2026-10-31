import assert from "node:assert/strict";
import { copyFileSync, mkdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { afterEach, beforeEach, describe, test } from "node:test";

import { CHANGELOG, GENERATED_MARKER, TITLE, addEntry, checkChangelog, entryVersion, newestEntry, entryFrom, parseEntries, preamble, releaseURL, writeAll, writeChangelog } from "./changelog.ts";
import { fakeData, fakeGitHub, type FakeData } from "./lib/fake-github.ts";
import { selectModule } from "./lib/modules.ts";
import { tempRepo, type TempRepo } from "./lib/temp-repo.ts";

const RELEASE = new URL("../release", import.meta.url).pathname;
const REPOSITORY = "example/limes";
const PGSTORE = "pgstore";

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

  // Each entry is written before its tag, so a release with none was cut
  // around the release pull request.
  test("check refuses a release with no entry", async () => {
    await writeAll(writer(), "HEAD");
    await repo.commit("feat: third");
    await repo.run("tag", "v0.3.0");
    await assert.rejects(checkChangelog(repo.dir, selectModule(repo.dir), "HEAD"), /v0\.3\.0 has no entry/);
  });

  // A module's first tag starts its line with no notes (lib/seed.ts).
  test("check passes a module's first release with no entry", async () => {
    repo.write(CHANGELOG, `${TITLE}\n\n## [0.2.0](b)\n`);
    assert.match(await checkChangelog(repo.dir, selectModule(repo.dir), "HEAD"), /lists 1 releases, each a tag, newest first$/);
    assert.match(await checkChangelog(repo.dir, selectModule(repo.dir, PGSTORE), "HEAD"), /lists 0 releases/);
  });

  // A release pull request adds the entry of the release it plans before
  // the release is tagged: the newest entry, above every release.
  test("check passes a newest entry above every release, still to be tagged", async () => {
    repo.write(CHANGELOG, `${TITLE}\n\n## [0.9.0](a)\n\n## [0.2.0](b)\n`);
    assert.match(await checkChangelog(repo.dir, selectModule(repo.dir), "HEAD"), /0\.9\.0 still to be tagged/);
  });

  test("check refuses an entry that is no tag, or out of order", async () => {
    repo.write(CHANGELOG, `${TITLE}\n\n## [0.1.5](a)\n`);
    await assert.rejects(checkChangelog(repo.dir, selectModule(repo.dir), "HEAD"), /lists 0\.1\.5, no release tag v0\.1\.5 is reachable/);
    repo.write(CHANGELOG, `${TITLE}\n\n## [0.9.0](a)\n\n## [0.8.0](b)\n`);
    await assert.rejects(checkChangelog(repo.dir, selectModule(repo.dir), "HEAD"), /lists 0\.8\.0, no release tag v0\.8\.0 is reachable/);
    repo.write(CHANGELOG, `${TITLE}\n\n## [0.1.0](a)\n\n## [0.2.0](b)\n`);
    await assert.rejects(checkChangelog(repo.dir, selectModule(repo.dir), "HEAD"), /lists 0\.2\.0, out of order/);
  });
});

describe("addEntry", () => {
  let repo: TempRepo;
  beforeEach(async () => {
    repo = await seeded();
  });
  afterEach(() => repo.cleanup());

  test("starts a changelog, then puts each entry first", () => {
    const module = selectModule(repo.dir);
    assert.equal(newestEntry(repo.dir, module), undefined);
    addEntry(repo.dir, module, "## [0.1.0](a)\n\n* one");
    addEntry(repo.dir, module, "## [0.2.0](b)\n\n* two");
    assert.equal(newestEntry(repo.dir, module), "## [0.2.0](b)\n\n* two");
    const text = readFileSync(join(repo.dir, CHANGELOG), "utf8");
    assert.ok(text.startsWith(preamble(module)));
    assert.deepEqual(headings(text), ["0.2.0", "0.1.0"]);
  });

  test("refuses an entry with no version, or one listed already", () => {
    const module = selectModule(repo.dir);
    assert.throws(() => addEntry(repo.dir, module, "* no heading"), /names no version/);
    addEntry(repo.dir, module, "## [0.1.0](a)");
    assert.throws(() => addEntry(repo.dir, module, "## [0.1.0](b)"), /lists 0\.1\.0 already/);
  });

  test("entryVersion reads the heading's version", () => {
    assert.equal(entryVersion("## [1.2.3](x) (2026-10-31)\n\n* a"), "1.2.3");
    assert.equal(entryVersion("* a"), undefined);
  });
});
