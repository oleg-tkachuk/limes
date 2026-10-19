import assert from "node:assert/strict";
import { copyFileSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, afterEach, before, beforeEach, describe, test } from "node:test";

import { fakeData, fakeGitHub, type FakeData } from "./lib/fake-github.ts";
import type { Module } from "./lib/modules.ts";
import { isReleaseTag, newReleaseTag, previousTag, proxyInfoURL, tagsOnHead, unpublishedTags, warmGoProxy } from "./lib/releases.ts";
import { tempRepo, type TempRepo } from "./lib/temp-repo.ts";
import { withoutWorkflowCommands } from "./lib/actions.ts";
import { GATES, releaseAll, type Release } from "./release.ts";

const REPO = "oleg-tkachuk/limes";
const RELEASE = new URL("../release/", import.meta.url).pathname;

test("isReleaseTag", () => {
  assert.equal(isReleaseTag("v0.2.0", ""), true);
  assert.equal(isReleaseTag("sub/v0.2.0", "sub/"), true);
  assert.equal(isReleaseTag("sub/v0.2.0", ""), false);
  assert.equal(isReleaseTag("vnext", ""), false);
  assert.equal(isReleaseTag("other/v1.0.0", "sub/"), false);
});

describe("tags on a repository", () => {
  let repo: TempRepo;
  let data: FakeData;
  beforeEach(async () => {
    repo = await tempRepo();
    data = fakeData();
    await repo.commit("feat: start");
    await repo.run("tag", "v0.1.0");
  });
  afterEach(() => repo.cleanup());

  test("newReleaseTag names only the tag a run added", async () => {
    const github = fakeGitHub(data);
    let before = await tagsOnHead(repo.dir);
    data.released.add("v0.1.0");
    assert.equal(await newReleaseTag(github, before, "", repo.dir), undefined, "no new tag on HEAD");
    await repo.commit("feat: more");
    await repo.run("tag", "notaversion");
    assert.equal(await newReleaseTag(github, [], "", repo.dir), undefined, "a tag that is no version");
    await repo.run("tag", "v0.2.0");
    assert.equal(await newReleaseTag(github, ["notaversion"], "", repo.dir), "v0.2.0", "the tag this run pushed");
    // The second dispatch: the tag is on HEAD from an earlier run.
    assert.equal(await newReleaseTag(github, ["v0.2.0"], "", repo.dir), "v0.2.0", "pushed earlier and never published");
    data.released.add("v0.2.0");
    assert.equal(await newReleaseTag(github, ["v0.2.0"], "", repo.dir), undefined, "pushed earlier and published");
    await repo.run("tag", "v0.2.1");
    assert.equal(await newReleaseTag(github, ["v0.2.0"], "", repo.dir), "v0.2.1", "a new tag beside an old one");
    // A nested module's run sees its own tags, and the root's run does not.
    before = ["v0.2.0", "v0.2.1"];
    data.released.add("v0.2.1");
    await repo.run("tag", "sub/v0.1.0");
    assert.equal(await newReleaseTag(github, before, "", repo.dir), undefined, "the root beside a nested module's new tag");
    assert.equal(await newReleaseTag(github, before, "sub/", repo.dir), "sub/v0.1.0", "the nested module's new tag");
    assert.equal(await newReleaseTag(github, before, "other/", repo.dir), undefined, "another module's prefix");
  });

  test("previousTag is the module's own", async () => {
    await repo.run("tag", "sub/v0.1.0");
    await repo.commit("fix: x");
    await repo.run("tag", "v0.2.0");
    await repo.run("tag", "sub/v0.2.0");
    assert.equal(await previousTag("v0.2.0", "", repo.dir), "v0.1.0");
    assert.equal(await previousTag("sub/v0.2.0", "sub/", repo.dir), "sub/v0.1.0");
    assert.equal(await previousTag("v0.1.0", "", repo.dir), undefined);
  });

  test("unpublishedTags names the tags below HEAD with no release, newest first", async () => {
    const github = fakeGitHub(data);
    assert.deepEqual(await unpublishedTags(github, "", repo.dir), ["v0.1.0"]);
    data.released.add("v0.1.0");
    assert.deepEqual(await unpublishedTags(github, "", repo.dir), [], "every tag published");
    await repo.commit("fix: a");
    await repo.run("tag", "v0.2.0");
    await repo.commit("fix: b");
    await repo.run("tag", "v0.3.0");
    await repo.run("tag", "v-not-a-version");
    await repo.commit("fix: c");
    assert.deepEqual(await unpublishedTags(github, "", repo.dir), ["v0.3.0", "v0.2.0"], "newest first, main moved on");
    await repo.run("checkout", "--quiet", "-b", "side", "HEAD~1");
    await repo.commit("side");
    await repo.run("tag", "v9.0.0");
    await repo.run("checkout", "--quiet", "main");
    assert.deepEqual(await unpublishedTags(github, "", repo.dir), ["v0.3.0", "v0.2.0"], "a tag not below HEAD");
    await repo.run("tag", "sub/v0.1.0");
    assert.deepEqual(await unpublishedTags(github, "sub/", repo.dir), ["sub/v0.1.0"], "the nested module's own");
  });
});

describe("the Go proxy", () => {
  test("asks under the module's own path for the version alone", () => {
    assert.equal(proxyInfoURL("v0.3.0", REPO, "file:///p"), `file:///p/github.com/${REPO}/@v/v0.3.0.info`);
    assert.equal(proxyInfoURL("pgstore/v0.3.0", REPO, "file:///p"), `file:///p/github.com/${REPO}/pgstore/@v/v0.3.0.info`);
  });
  test("refuses a tag that is no version, and an upper-case path", () => {
    assert.throws(() => proxyInfoURL("latest", REPO));
    assert.throws(() => proxyInfoURL("v1.2", REPO));
    assert.throws(() => proxyInfoURL("v0.3.0", "Oleg/limes"));
  });
  test("tries again, then warns rather than fails", async () => {
    let calls = 0;
    const flaky = async () => ({ ok: ++calls >= 3, status: calls >= 3 ? 200 : 404 });
    assert.equal(await warmGoProxy("v0.3.0", REPO, flaky, 0), true);
    assert.equal(calls, 3);
    assert.equal(await warmGoProxy("v0.3.0", REPO, async () => ({ ok: false, status: 404 }), 0), false);
  });
});

test("withoutWorkflowCommands resumes with the token it stopped with, also on failure", async () => {
  const printed: string[] = [];
  const log = console.log;
  console.log = (line: string) => printed.push(line);
  try {
    await assert.rejects(withoutWorkflowCommands(async () => Promise.reject(new Error("boom"))));
  } finally {
    console.log = log;
  }
  const token = /^::stop-commands::([0-9a-f]{32})$/.exec(printed[0] ?? "")?.[1];
  assert.ok(token !== undefined, printed.join("\n"));
  assert.equal(printed[1], `::${token}::`);
});

describe("releaseAll", () => {
  let repo: TempRepo;
  let data: FakeData;
  let notesDir: string;
  // next is the tag each module's semantic-release pushes this run, if any.
  let next: Record<string, string | undefined>;
  let gatesPassing: boolean;
  let rootTag: string | undefined;
  let failing: string | undefined;

  before(async () => {
    notesDir = mkdtempSync(join(tmpdir(), "limes-notes-"));
  });
  after(() => rmSync(notesDir, { recursive: true, force: true }));
  beforeEach(async () => {
    repo = await tempRepo();
    mkdirSync(join(repo.dir, ".github/release"), { recursive: true });
    copyFileSync(join(RELEASE, "modules.cjs"), join(repo.dir, ".github/release/modules.cjs"));
    await repo.commit("feat: start");
    data = fakeData();
    next = {};
    gatesPassing = true;
    rootTag = undefined;
    failing = undefined;
  });
  afterEach(() => repo.cleanup());

  function release(): Release {
    return {
      github: fakeGitHub(data),
      repository: REPO,
      root: repo.dir,
      async runSemanticRelease(module: Module, notesFile: string) {
        if (module.name === failing) {
          throw new Error("semantic-release failed");
        }
        const tag = next[module.name];
        if (tag !== undefined) {
          await repo.run("tag", tag);
          data.tags.add(tag);
          writeFileSync(notesFile, `notes of ${tag}\n`);
        }
      },
      gatePassed: async () => gatesPassing,
      fetchInfo: async () => ({ ok: true, status: 200 }),
      notesDir,
      onRootTag: (tag) => {
        rootTag = tag;
      },
    };
  }

  test("each module releases its own tag, the root's marked Latest", async () => {
    next = { limes: "v0.2.0", redisreplay: "redisreplay/v0.1.0", pgstore: "pgstore/v0.1.0" };
    assert.equal(await releaseAll(release()), true);
    assert.equal(rootTag, "v0.2.0");
    assert.deepEqual(
      data.published.map((r) => [r.tag, r.latest, r.notes]),
      [
        ["v0.2.0", true, "notes of v0.2.0\n"],
        ["redisreplay/v0.1.0", false, "notes of redisreplay/v0.1.0\n"],
        ["pgstore/v0.1.0", false, "notes of pgstore/v0.1.0\n"],
      ],
    );
  });

  test("a gated module waits while its CI job did not pass", async () => {
    gatesPassing = false;
    next = { limes: "v0.2.0", pgstore: "pgstore/v0.1.0" };
    await releaseAll(release());
    assert.deepEqual(
      data.published.map((r) => r.tag),
      ["v0.2.0"],
    );
    assert.ok(GATES.pgstore !== undefined);
  });

  test("no release due publishes nothing", async () => {
    assert.equal(await releaseAll(release()), true);
    assert.deepEqual(data.published, []);
    assert.equal(rootTag, undefined);
  });

  test("one module failing leaves the others released, and fails the run", async () => {
    failing = "limes";
    next = { redisreplay: "redisreplay/v0.1.0" };
    assert.equal(await releaseAll(release()), false);
    assert.deepEqual(
      data.published.map((r) => r.tag),
      ["redisreplay/v0.1.0"],
    );
  });

  test("a tag an earlier run left behind is published, never as Latest", async () => {
    await repo.run("tag", "v0.1.0");
    data.tags.add("v0.1.0");
    await repo.commit("fix: later");
    assert.equal(await releaseAll(release()), true);
    assert.deepEqual(
      data.published.map((r) => [r.tag, r.latest, r.previousTag]),
      [["v0.1.0", false, undefined]],
    );
  });

  test("the root's tag is known before its release is published", async () => {
    next = { limes: "v0.2.0" };
    const r = release();
    const publishRelease = r.github.publishRelease.bind(r.github);
    r.github.publishRelease = async (rel) => {
      assert.equal(rootTag, rel.tag);
      return publishRelease(rel);
    };
    await releaseAll(r);
  });
});
