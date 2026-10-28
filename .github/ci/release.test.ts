import assert from "node:assert/strict";
import { copyFileSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, afterEach, before, beforeEach, describe, test } from "node:test";

import { fakeData, fakeGitHub, type FakeData } from "./lib/fake-github.ts";
import type { Module } from "./lib/modules.ts";
import { isReleaseTag, isStableReleaseTag, newReleaseTag, previousTag, proxyInfoURL, tagsOnHead, unpublishedTags, warmGoProxy } from "./lib/releases.ts";
import { tempRepo, type TempRepo } from "./lib/temp-repo.ts";
import { withoutWorkflowCommands } from "./lib/actions.ts";
import { releaseCommits } from "./lib/modules.ts";
import { ALL_MODULES, candidateNotesBody, Channel, GATED, gateOf, GATES, type Release, releaseAll, releaseOptions, semanticReleaseCommand, unchangedSince } from "./release.ts";
import { sinceCandidate } from "./lib/prerelease.ts";

const REPO = "oleg-tkachuk/limes";
const RELEASE = new URL("../release/", import.meta.url).pathname;

test("isReleaseTag", () => {
  assert.equal(isReleaseTag("v0.2.0", ""), true);
  assert.equal(isReleaseTag("sub/v0.2.0", "sub/"), true);
  assert.equal(isReleaseTag("sub/v0.2.0", ""), false);
  assert.equal(isReleaseTag("vnext", ""), false);
  assert.equal(isReleaseTag("other/v1.0.0", "sub/"), false);
  assert.equal(isReleaseTag("v0.2.0-rc.1", ""), true);
});

test("isStableReleaseTag", () => {
  assert.equal(isStableReleaseTag("v0.2.0", ""), true);
  assert.equal(isStableReleaseTag("sub/v0.2.0", "sub/"), true);
  assert.equal(isStableReleaseTag("v0.2.0-rc.1", ""), false);
  assert.equal(isStableReleaseTag("sub/v0.2.0-rc.3", "sub/"), false);
  assert.equal(isStableReleaseTag("sub/v0.2.0", ""), false);
});

// CANDIDATE_NOTES opens the notes the stand-in dry run writes.
const CANDIDATE_NOTES = "candidate notes";

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

  // A release candidate left on HEAD with no GitHub release, by a
  // prerelease run whose publish failed, is never taken for a release that
  // run left behind: it would be published as a release and marked Latest.
  test("newReleaseTag never names a release candidate", async () => {
    const github = fakeGitHub(data);
    data.released.add("v0.1.0");
    await repo.commit("feat: more");
    await repo.run("tag", "v0.2.0-rc.1");
    assert.equal(await newReleaseTag(github, [], "", repo.dir), undefined, "a candidate this run would have pushed");
    assert.equal(await newReleaseTag(github, ["v0.2.0-rc.1"], "", repo.dir), undefined, "a candidate left unpublished");
  });

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

test("withoutWorkflowCommands resumes with the token it stopped with, on both streams, also on failure", async () => {
  const printed: string[] = [];
  const failed: string[] = [];
  const log = console.log;
  const err = console.error;
  console.log = (line: string) => printed.push(line);
  console.error = (line: string) => failed.push(line);
  try {
    await assert.rejects(withoutWorkflowCommands(async () => Promise.reject(new Error("boom"))));
  } finally {
    console.log = log;
    console.error = err;
  }
  const token = /^::stop-commands::([0-9a-f]{32})$/.exec(printed[0] ?? "")?.[1];
  assert.ok(token !== undefined, printed.join("\n"));
  assert.equal(printed[1], `::${token}::`);
  assert.deepEqual(failed, printed);
});

describe("releaseAll", () => {
  let repo: TempRepo;
  let data: FakeData;
  let notesDir: string;
  // next is the tag each module's semantic-release pushes this run, if any.
  let next: Record<string, string | undefined>;
  let gatesPassing: boolean;
  // failedGates are the gating jobs that did not pass, beside gatesPassing.
  let failedGates: Set<string>;
  let rootTag: string | undefined;
  let failing: string | undefined;
  let noCandidateNotes = false;

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
    failedGates = new Set();
    rootTag = undefined;
    failing = undefined;
    noCandidateNotes = false;
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
      async candidateNotes(module: Module, notesFile: string) {
        if (!noCandidateNotes) {
          writeFileSync(notesFile, `${CANDIDATE_NOTES} of ${module.name}\n`);
        }
      },
      gatePassed: async (job) => gatesPassing && !failedGates.has(job),
      fetchInfo: async () => ({ ok: true, status: 200 }),
      notesDir,
      onRootTag: (tag) => {
        rootTag = tag;
      },
      channel: Channel.Release,
      only: undefined,
      manual: true,
      nextMajor: false,
      policy: { minorCommits: 3, patchCommits: 5, immediateTypes: ["security", "build"] },
      commits: releaseCommits(),
      pushTag: async (tag) => {
        data.tags.add(tag);
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

  // Every nested module is gated, each by its own job: one failing holds
  // back that module alone.
  test("each gated module waits for its own CI job alone", async () => {
    failedGates = new Set([gateOf("redisreplay")]);
    next = {
      limes: "v0.2.0", redisreplay: "redisreplay/v0.1.0", pgstore: "pgstore/v0.1.0", otelmetric: "otelmetric/v0.1.0", exchange: "exchange/v0.1.0",
    };
    await releaseAll(release());
    assert.deepEqual(
      data.published.map((r) => r.tag),
      ["v0.2.0", "pgstore/v0.1.0", "otelmetric/v0.1.0", "exchange/v0.1.0"],
    );
    assert.deepEqual([...GATED].sort(), ["exchange", "otelmetric", "pgstore", "redisreplay"]);
  });

  test("the run's summary says what happened to each module", async () => {
    const page = join(notesDir, "summary.md");
    process.env.GITHUB_STEP_SUMMARY = page;
    try {
      next = { limes: "v0.2.0" };
      gatesPassing = false;
      await releaseAll(release());
    } finally {
      delete process.env.GITHUB_STEP_SUMMARY;
    }
    const { readFileSync } = await import("node:fs");
    const summary = readFileSync(page, "utf8");
    assert.match(summary, /\| limes \| v0\.2\.0 \| released by hand \|/);
    assert.match(summary, /\| pgstore \|  \| 'Released limes \(pgstore\)' did not pass; not released this run \|/);
    assert.match(summary, /\| redisreplay \|  \| 'Released limes \(redisreplay\)' did not pass; not released this run \|/);
  });

  // A run by hand that released nothing says so as an annotation, naming
  // each module's last release; one that released something does not.
  async function noticesOf(run: () => Promise<unknown>): Promise<string[]> {
    const printed: string[] = [];
    const log = console.log;
    console.log = (line: string) => void printed.push(line);
    try {
      await run();
    } finally {
      console.log = log;
    }
    return printed.filter((l) => l.startsWith("notice: ") || l.startsWith("::notice::"));
  }
  test("a run by hand that released nothing says so", async () => {
    await repo.run("tag", "v0.1.0");
    data.released.add("v0.1.0");
    const notices = await noticesOf(() => releaseAll({ ...release(), channel: Channel.Prerelease }));
    assert.equal(notices.length, 1, notices.join("\n"));
    assert.match(notices[0] ?? "", /nothing pre-released: limes: no commit since v0\.1\.0 cuts a release, so no candidate/);
  });
  test("a run by hand that released something says nothing more", async () => {
    next = { limes: "v0.2.0" };
    assert.deepEqual(await noticesOf(() => releaseAll(release())), []);
  });
  test("a run nobody asked for that released nothing says nothing more", async () => {
    assert.deepEqual(await noticesOf(() => releaseAll({ ...release(), manual: false })), []);
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

  // A release nobody asked for waits for the policy's thresholds.
  describe("by the policy", () => {
    async function commits(...subjects: string[]): Promise<void> {
      for (const subject of subjects) {
        repo.write("x.go", `// ${subject}\n`);
        await repo.commit(subject);
      }
    }
    async function released(): Promise<boolean> {
      next = { limes: "v0.2.0" };
      await releaseAll({ ...release(), manual: false });
      return data.published.some((r) => r.tag === "v0.2.0");
    }
    beforeEach(async () => {
      await repo.run("tag", "v0.1.0");
      data.released.add("v0.1.0");
    });

    test("two features are not due", async () => {
      await commits("feat: a", "feat: b", "docs: c");
      assert.equal(await released(), false);
    });
    test("three features are", async () => {
      await commits("feat: a", "feat: b", "feat: c");
      assert.equal(await released(), true);
    });
    test("four fixes are not due", async () => {
      await commits("fix: a", "fix: b", "perf: c", "fix: d");
      assert.equal(await released(), false);
    });
    test("five fixes are", async () => {
      await commits("fix: a", "fix: b", "perf: c", "fix: d", "fix: e");
      assert.equal(await released(), true);
    });
    test("a security fix is released at once", async () => {
      await commits("security: seal");
      assert.equal(await released(), true);
    });
    test("a breaking change is released at once", async () => {
      await commits("feat!: change");
      assert.equal(await released(), true);
    });
    test("commits before the last release are not counted", async () => {
      await commits("feat: a", "feat: b");
      await repo.run("tag", "v0.1.1");
      data.released.add("v0.1.1");
      await commits("feat: c");
      assert.equal(await released(), false);
    });
    test("a merge commit does not count its pull request twice", async () => {
      await repo.run("checkout", "--quiet", "-b", "topic");
      await commits("feat: a", "feat: b");
      await repo.run("checkout", "--quiet", "main");
      await repo.run("-c", "user.name=t", "-c", "user.email=t@example.invalid", "merge", "--quiet", "--no-ff", "-m", "feat: a and b", "topic");
      assert.equal(await released(), false);
    });
    test("a release by hand ignores the thresholds", async () => {
      await commits("fix: a");
      next = { limes: "v0.1.1" };
      await releaseAll({ ...release(), manual: true });
      assert.deepEqual(data.published.map((r) => r.tag), ["v0.1.1"]);
    });
  });

  describe("release candidates", () => {
    beforeEach(async () => {
      await repo.run("tag", "v0.1.0");
      data.released.add("v0.1.0");
      data.tags.add("v0.1.0");
    });
    async function candidate(only = "limes"): Promise<string[]> {
      await releaseAll({ ...release(), channel: Channel.Prerelease, only });
      return data.published.filter((r) => r.prerelease === true).map((r) => r.tag);
    }

    test("the next version, as rc.1, then rc.2", async () => {
      repo.write("x.go", "// a\n");
      await repo.commit("feat: a");
      assert.deepEqual(await candidate(), ["v0.2.0-rc.1"]);
      assert.equal(data.published[0]?.latest, false);
      repo.write("x.go", "// b\n");
      await repo.commit("fix: b");
      assert.deepEqual(await candidate(), ["v0.2.0-rc.1", "v0.2.0-rc.2"]);
    });
    test("a candidate whose dry run releases nothing is not tagged", async () => {
      repo.write("x.go", "// a\n");
      await repo.commit("feat: a");
      noCandidateNotes = true;
      const printed: string[] = [];
      const log = console.log;
      console.log = (line: string) => void printed.push(line);
      try {
        assert.equal(await releaseAll({ ...release(), channel: Channel.Prerelease, only: "limes" }), false);
      } finally {
        console.log = log;
      }
      assert.match(printed.join("\n"), /releases nothing/);
      assert.equal(data.tags.has("v0.2.0-rc.1"), false);
      assert.deepEqual(data.published, []);
    });
    test("a candidate is published with the notes of the release it stands for", async () => {
      repo.write("x.go", "// a\n");
      await repo.commit("feat: a");
      await candidate();
      assert.equal(data.published[0]?.notes, `${CANDIDATE_NOTES} of limes\n`);
    });
    test("a candidate of the next major: v1.0.0-rc.N, breaking or not between them", async () => {
      repo.write("x.go", "// a\n");
      await repo.commit("fix: a");
      const major = async () => {
        await releaseAll({ ...release(), channel: Channel.Prerelease, only: "limes", nextMajor: true });
        return data.published.filter((r) => r.prerelease === true).map((r) => r.tag);
      };
      assert.deepEqual(await major(), ["v1.0.0-rc.1"]);
      repo.write("x.go", "// b\n");
      await repo.commit("feat!: break the API before 1.0");
      assert.deepEqual(await major(), ["v1.0.0-rc.1", "v1.0.0-rc.2"]);
      // The next release by the rules on main is still 0.x.
      assert.deepEqual(await candidate(), ["v1.0.0-rc.1", "v1.0.0-rc.2", "v0.2.0-rc.1"]);
    });
    // A candidate of the next major is cut beside the releases main goes on
    // making: its notes start from the candidate before it, naming the
    // releases between, and GitHub lists the pull requests since that
    // candidate, not since the last release.
    test("a candidate of the next major tells what changed since the one before", async () => {
      const major = async () => {
        await releaseAll({ ...release(), channel: Channel.Prerelease, only: "limes", nextMajor: true });
        return data.published.filter((r) => r.prerelease === true);
      };
      repo.write("x.go", "// a\n");
      await repo.commit("fix: a");
      const [first] = await major();
      assert.equal(first?.tag, "v1.0.0-rc.1");
      assert.equal(first?.previousTag, "v0.1.0");
      assert.equal(first?.notes, `${CANDIDATE_NOTES} of limes\n`);
      for (const [file, subject, tag] of [["y.go", "feat: b", "v0.2.0"], ["z.go", "fix: c", "v0.2.1"]] as const) {
        repo.write(file, "// x\n");
        await repo.commit(subject);
        await repo.run("tag", tag);
        data.tags.add(tag);
      }
      noCandidateNotes = true;
      const second = (await major())[1];
      assert.equal(second?.tag, "v1.0.0-rc.2");
      assert.equal(second?.previousTag, "v1.0.0-rc.1");
      assert.equal(
        second?.notes,
        `${sinceCandidate("v1.0.0-rc.1", ["v0.2.0", "v0.2.1"], REPO)}${unchangedSince("v0.2.1")}`,
      );
      assert.match(second?.notes ?? "", /^Since v1\.0\.0-rc\.1: \[v0\.2\.0\]\(https:\/\/github\.com\/oleg-tkachuk\/limes\/releases\/tag\/v0\.2\.0\), \[v0\.2\.1\]/);
    });
    test("a candidate after one with no release between adds nothing before its notes", async () => {
      repo.write("x.go", "// a\n");
      await repo.commit("feat: a");
      await candidate();
      repo.write("x.go", "// b\n");
      await repo.commit("fix: b");
      await candidate();
      const second = data.published[1];
      assert.equal(second?.tag, "v0.2.0-rc.2");
      assert.equal(second?.previousTag, "v0.2.0-rc.1");
      assert.equal(second?.notes, `${CANDIDATE_NOTES} of limes\n`);
    });
    test("a candidate of the next major with nothing pending is the last release's code", async () => {
      noCandidateNotes = true;
      assert.equal(await releaseAll({ ...release(), channel: Channel.Prerelease, only: "limes", nextMajor: true }), true);
      assert.deepEqual(data.published.map((r) => [r.tag, r.notes]), [["v1.0.0-rc.1", unchangedSince("v0.1.0")]]);
    });
    test("a candidate of the next major is limes's alone, and a prerelease", async () => {
      repo.write("x.go", "// a\n");
      await repo.commit("fix: a");
      await releaseAll({ ...release(), channel: Channel.Prerelease, nextMajor: true });
      assert.deepEqual(data.published.map((r) => r.tag), ["v1.0.0-rc.1"]);
      await assert.rejects(releaseAll({ ...release(), nextMajor: true }), /prerelease/);
    });
    test("a candidate's notes drop the heading naming the version by main's rules", () => {
      const notes = "## [0.2.0](https://github.com/o/r/compare/v0.1.0...v0.2.0) (2026-10-24)\n\n### Features\n\n* a\n";
      assert.equal(candidateNotesBody(notes), "### Features\n\n* a\n");
      assert.equal(candidateNotesBody("### Features\n"), "### Features\n");
    });
    test("a patch's candidate", async () => {
      repo.write("x.go", "// a\n");
      await repo.commit("fix: a");
      assert.deepEqual(await candidate(), ["v0.1.1-rc.1"]);
    });
    test("nothing pending, no candidate", async () => {
      assert.deepEqual(await candidate(), []);
    });
    test("a candidate is no release line for the next stable release", async () => {
      repo.write("x.go", "// a\n");
      await repo.commit("feat: a");
      await candidate();
      const { lastStableTag } = await import("./lib/release-policy.ts");
      const { selectModule } = await import("./lib/modules.ts");
      assert.equal(await lastStableTag(selectModule(repo.dir), "HEAD", repo.dir), "v0.1.0");
    });
    test("an unknown module is refused", async () => {
      await assert.rejects(releaseAll({ ...release(), channel: Channel.Prerelease, only: "nope" }));
    });
  });
});

// A run by hand offers every module of the table, all of them, and each
// channel the release knows, and no other.
test("release.yaml's inputs are the table's modules and the channels", async () => {
  const { readFileSync } = await import("node:fs");
  const { parse } = await import("yaml");
  const { releasedModules } = await import("./lib/modules.ts");
  const { ALL_MODULES } = await import("./release.ts");
  const workflow = parse(readFileSync(new URL("../workflows/release.yaml", import.meta.url), "utf8")) as {
    on: { workflow_dispatch: { inputs: Record<string, { options: string[] }> } };
  };
  const inputs = workflow.on.workflow_dispatch.inputs;
  const root = new URL("../../", import.meta.url).pathname;
  assert.deepEqual([...(inputs.module?.options ?? [])].sort(), [ALL_MODULES, ...releasedModules(root)].sort());
  assert.deepEqual([...(inputs.channel?.options ?? [])].sort(), Object.values(Channel).sort());
});

// The release runs the semantic-release npm ci installed from the lockfile,
// and nothing npx could fetch.
test("semantic-release runs from the release tooling's own install", () => {
  const [file, args] = semanticReleaseCommand("/repo");
  assert.equal(file, "/repo/.github/release/node_modules/.bin/semantic-release");
  assert.deepEqual(args, ["--no-ci"]);
});

// release.ts hands semantic-release its push URL under the name modules.cjs
// reads, which takes it as repositoryUrl, and the repository's HTTPS URL
// when it is unset.
test("semantic-release pushes to the URL release.ts hands it", async () => {
  const { createRequire } = await import("node:module");
  const { PUSH_URL_ENV } = await import("./release.ts");
  const path = new URL("../release/modules.cjs", import.meta.url).pathname;
  const require = createRequire(import.meta.url);
  const table = require(path) as { PUSH_URL_ENV: string; ROOT_MODULE: string; configFor(name: string): { repositoryUrl: string } };
  assert.equal(table.PUSH_URL_ENV, PUSH_URL_ENV);
  const saved = process.env[PUSH_URL_ENV];
  try {
    process.env[PUSH_URL_ENV] = "git@github.com:o/r.git";
    assert.equal(table.configFor(table.ROOT_MODULE).repositoryUrl, "git@github.com:o/r.git");
    delete process.env[PUSH_URL_ENV];
    assert.match(table.configFor(table.ROOT_MODULE).repositoryUrl, /^https:\/\/github\.com\//);
  } finally {
    if (saved === undefined) delete process.env[PUSH_URL_ENV];
    else process.env[PUSH_URL_ENV] = saved;
  }
});

// The release step has the deploy key, by the name release.ts reads, from
// the secret of the same name.
test("release.yaml hands the release its deploy key", async () => {
  const { readFileSync } = await import("node:fs");
  const { parse } = await import("yaml");
  const { DEPLOY_KEY_ENV } = await import("./lib/deploy-key.ts");
  const workflow = parse(readFileSync(new URL("../workflows/release.yaml", import.meta.url), "utf8")) as {
    jobs: Record<string, { steps: { id?: string; env?: Record<string, string> }[] }>;
  };
  const step = Object.values(workflow.jobs).flatMap((j) => j.steps).find((s) => s.id === "modules");
  assert.equal(step?.env?.[DEPLOY_KEY_ENV], `\${{ secrets.${DEPLOY_KEY_ENV} }}`);
});

describe("releaseOptions", () => {
  const envOf = (vars: Record<string, string>) => (name: string) => vars[name];
  test("a run that names nothing releases every module on the release channel", () => {
    assert.deepEqual(releaseOptions(envOf({})), { channel: Channel.Release, manual: false, nextMajor: false, only: undefined });
  });
  test("a run by hand of one module's candidate of the next major", () => {
    assert.deepEqual(releaseOptions(envOf({ CHANNEL: Channel.Prerelease, MANUAL: "true", NEXT_MAJOR: "true", MODULE: "limes" })), {
      channel: Channel.Prerelease, manual: true, nextMajor: true, only: "limes",
    });
  });
  test("every module, named as such", () => {
    assert.equal(releaseOptions(envOf({ MODULE: ALL_MODULES })).only, undefined);
  });
  test("refuses a channel it does not cut", () => {
    assert.throws(() => releaseOptions(envOf({ CHANNEL: "nightly" })), /neither release nor prerelease/);
  });
  test("refuses a prerelease not cut by hand", () => {
    assert.throws(() => releaseOptions(envOf({ CHANNEL: Channel.Prerelease })), /by hand only/);
  });
});
