import assert from "node:assert/strict";
import { copyFileSync, mkdirSync } from "node:fs";
import { join } from "node:path";
import { afterEach, beforeEach, describe, test } from "node:test";

import { CHANGELOG, TITLE } from "./changelog.ts";
import { fakeData, fakeGitHub, type FakeData } from "./lib/fake-github.ts";
import { tempRepo, type TempRepo } from "./lib/temp-repo.ts";
import { tagReleases, type Tagger } from "./release-tag.ts";

const RELEASE = new URL("../release", import.meta.url).pathname;
const REPOSITORY = "example/limes";
const PGSTORE = "pgstore";
// IDENTITY signs the merge, which tempRepo's commit does not make.
const IDENTITY = ["-c", "user.name=test", "-c", "user.email=test@example.invalid"];
// NO_PAUSE: the Go proxy is asked once, with no wait.
const NO_PAUSE = 0;

// changelog is a changelog of entries, newest first.
function changelog(...entries: string[]): string {
  return `${TITLE}\n\nIntro.\n\n${entries.join("\n\n")}\n`;
}

describe("tagReleases", () => {
  let repo: TempRepo;
  let data: FakeData;
  let release: string;
  const proxied: string[] = [];

  // main with limes v0.1.0 and pgstore/v0.1.0 listed in their changelogs,
  // then a release pull request adding v0.2.0 and pgstore/v0.1.1 merged,
  // then a commit after it.
  beforeEach(async () => {
    repo = await tempRepo();
    mkdirSync(join(repo.dir, ".github/release"), { recursive: true });
    copyFileSync(join(RELEASE, "modules.cjs"), join(repo.dir, ".github/release/modules.cjs"));
    repo.write("go.mod", "module example.com/limes\n");
    repo.write(`${PGSTORE}/go.mod`, "module example.com/limes/pgstore\n");
    repo.write(CHANGELOG, changelog("## [0.1.0](a)"));
    repo.write(`${PGSTORE}/${CHANGELOG}`, changelog("## [0.1.0](b)"));
    await repo.commit("init");
    await repo.run("tag", "v0.1.0");
    await repo.run("tag", `${PGSTORE}/v0.1.0`);
    await repo.run("switch", "--quiet", "-c", "release/next");
    repo.write(CHANGELOG, changelog("## [0.2.0](c)\n\n* two", "## [0.1.0](a)"));
    repo.write(`${PGSTORE}/${CHANGELOG}`, changelog("## [0.1.1](d)\n\n* require limes v0.2.0", "## [0.1.0](b)"));
    await repo.commit("chore(release): v0.2.0, pgstore/v0.1.1");
    await repo.run("switch", "--quiet", "main");
    await repo.run(...IDENTITY, "merge", "--quiet", "--no-ff", "-m", "Merge pull request #1 from example/release/next", "release/next");
    release = (await repo.run("rev-parse", "HEAD")).trim();
    repo.write("later.go", "package limes\n");
    await repo.commit("feat: later");
    data = fakeData();
    for (const tag of ["v0.1.0", `${PGSTORE}/v0.1.0`]) {
      data.tags.add(tag);
      data.released.add(tag);
    }
    proxied.length = 0;
  });
  afterEach(() => repo.cleanup());

  function tagger(): Tagger {
    return {
      github: fakeGitHub(data),
      repository: REPOSITORY,
      root: repo.dir,
      pauseMs: NO_PAUSE,
      fetchInfo: async (url) => {
        proxied.push(url);
        return { ok: true, status: 200 };
      },
      pushTag: async (tag) => void data.tags.add(tag),
    };
  }

  const tagged = async (tag: string) => (await repo.run("rev-list", "-n", "1", tag)).trim();

  test("tags each module's new entry on the release's merge commit, and publishes the entry", async () => {
    assert.deepEqual(await tagReleases(tagger()), ["v0.2.0", `${PGSTORE}/v0.1.1`]);
    assert.equal(await tagged("v0.2.0"), release);
    assert.equal(await tagged(`${PGSTORE}/v0.1.1`), release);
    assert.deepEqual(
      data.published.map((r) => [r.tag, r.notes, r.latest, r.prerelease]),
      [
        ["v0.2.0", "## [0.2.0](c)\n\n* two", true, false],
        [`${PGSTORE}/v0.1.1`, "## [0.1.1](d)\n\n* require limes v0.2.0", false, false],
      ],
    );
    assert.equal(proxied.length, 2);
  });

  test("tags nothing once every entry is tagged", async () => {
    await tagReleases(tagger());
    data.published.length = 0;
    assert.deepEqual(await tagReleases(tagger()), []);
    assert.deepEqual(data.published, []);
  });

  test("refuses an entry no newer than the module's last release", async () => {
    await repo.run("tag", "v0.3.0", "HEAD~1");
    await assert.rejects(tagReleases(tagger()), /0\.2\.0 is not past v0\.3\.0/);
  });
});
