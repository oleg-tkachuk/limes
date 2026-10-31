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
import { ALL_MODULES, candidateNotesBody, Channel, type Release, releaseAll, releaseOptions, semanticReleaseCommand, unchangedSince } from "./release.ts";
import { sinceCandidate } from "./lib/prerelease.ts";
import { API_HEADING } from "./lib/candidate-releases.ts";

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
    noCandidateNotes = false;
  });
  afterEach(() => repo.cleanup());

  function release(): Release {
    return {
      github: fakeGitHub(data),
      repository: REPO,
      root: repo.dir,
      async candidateNotes(module: Module, notesFile: string) {
        if (!noCandidateNotes) {
          writeFileSync(notesFile, `${CANDIDATE_NOTES} of ${module.name}\n`);
        }
      },
      fetchInfo: async () => ({ ok: true, status: 200 }),
      notesDir,
      only: undefined,
      nextMajor: false,
      commits: releaseCommits(),
      pushTag: async (tag) => {
        data.tags.add(tag);
      },
    };
  }

  describe("release candidates", () => {
    beforeEach(async () => {
      await repo.run("tag", "v0.1.0");
      data.released.add("v0.1.0");
      data.tags.add("v0.1.0");
    });
    async function candidate(only = "limes"): Promise<string[]> {
      await releaseAll({ ...release(), only });
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
        assert.equal(await releaseAll({ ...release(), only: "limes" }), false);
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
        await releaseAll({ ...release(), only: "limes", nextMajor: true });
        return data.published.filter((r) => r.prerelease === true).map((r) => r.tag);
      };
      assert.deepEqual(await major(), ["v1.0.0-rc.1"]);
      // Its notes say whose API it is: limes's release before it, past which
      // the fix goes (+), and the nested modules', none released here.
      const rc1 = data.published.find((r) => r.tag === "v1.0.0-rc.1")?.notes ?? "";
      assert.match(rc1, /\| limes \| redisreplay \| pgstore \| otelmetric \| exchange \| biscuit \|/);
      assert.match(rc1, /\| `v0\.1\.0`\+ \| — \| — \| — \| — \| — \|/);
      repo.write("x.go", "// b\n");
      await repo.commit("feat!: break the API before 1.0");
      assert.deepEqual(await major(), ["v1.0.0-rc.1", "v1.0.0-rc.2"]);
      // The next release by the rules on main is still 0.x.
      assert.deepEqual(await candidate(), ["v1.0.0-rc.1", "v1.0.0-rc.2", "v0.2.0-rc.1"]);
      assert.doesNotMatch(data.published.find((r) => r.tag === "v0.2.0-rc.1")?.notes ?? "", /\| limes \|/, "a candidate of the next version names no API");
    });
    // A candidate of the next major is cut beside the releases main goes on
    // making: its notes start from the candidate before it, naming the
    // releases between, and GitHub lists the pull requests since that
    // candidate, not since the last release.
    test("a candidate of the next major tells what changed since the one before", async () => {
      const major = async () => {
        await releaseAll({ ...release(), only: "limes", nextMajor: true });
        return data.published.filter((r) => r.prerelease === true);
      };
      repo.write("x.go", "// a\n");
      await repo.commit("fix: a");
      const [first] = await major();
      assert.equal(first?.tag, "v1.0.0-rc.1");
      assert.equal(first?.previousTag, "v0.1.0");
      assert.ok(first?.notes.startsWith(`${CANDIDATE_NOTES} of limes\n\n${API_HEADING}`), first?.notes);
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
      assert.ok(second?.notes.startsWith(`${sinceCandidate("v1.0.0-rc.1", ["v0.2.0", "v0.2.1"], REPO)}${unchangedSince("v0.2.1")}\n${API_HEADING}`), second?.notes);
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
      assert.equal(await releaseAll({ ...release(), only: "limes", nextMajor: true }), true);
      assert.deepEqual(data.published.map((r) => r.tag), ["v1.0.0-rc.1"]);
      assert.ok(data.published[0]?.notes.startsWith(`${unchangedSince("v0.1.0")}\n${API_HEADING}`), data.published[0]?.notes);
      assert.match(data.published[0]?.notes ?? "", /\| `v0\.1\.0` \|/, "the last release's code, so no +");
    });
    test("a candidate of the next major is limes's alone, and a prerelease", async () => {
      repo.write("x.go", "// a\n");
      await repo.commit("fix: a");
      await releaseAll({ ...release(), nextMajor: true });
      assert.deepEqual(data.published.map((r) => r.tag), ["v1.0.0-rc.1"]);
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
      await assert.rejects(releaseAll({ ...release(), only: "nope" }));
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

// The steps that push tags have the deploy key, by the name they read,
// from the secret of the same name.
test("release.yaml hands the tag and the candidates their deploy key", async () => {
  const { readFileSync } = await import("node:fs");
  const { parse } = await import("yaml");
  const { DEPLOY_KEY_ENV } = await import("./lib/deploy-key.ts");
  const workflow = parse(readFileSync(new URL("../workflows/release.yaml", import.meta.url), "utf8")) as {
    jobs: Record<string, { steps: { id?: string; env?: Record<string, string> }[] }>;
  };
  for (const id of ["tag", "candidates"]) {
    const step = Object.values(workflow.jobs).flatMap((j) => j.steps).find((s) => s.id === id);
    assert.equal(step?.env?.[DEPLOY_KEY_ENV], `\${{ secrets.${DEPLOY_KEY_ENV} }}`, id);
  }
});

describe("releaseOptions", () => {
  const envOf = (vars: Record<string, string>) => (name: string) => vars[name];
  const byHand = { CHANNEL: Channel.Prerelease, MANUAL: "true" };
  test("a candidate of every module", () => {
    assert.deepEqual(releaseOptions(envOf(byHand)), { nextMajor: false, only: undefined });
  });
  test("a run by hand of one module's candidate of the next major", () => {
    assert.deepEqual(releaseOptions(envOf({ ...byHand, NEXT_MAJOR: "true", MODULE: "limes" })), { nextMajor: true, only: "limes" });
  });
  test("every module, named as such", () => {
    assert.equal(releaseOptions(envOf({ ...byHand, MODULE: ALL_MODULES })).only, undefined);
  });
  test("refuses a release, which release-plan.ts proposes, and any other channel", () => {
    assert.throws(() => releaseOptions(envOf({ ...byHand, CHANNEL: Channel.Release })), /candidates alone/);
    assert.throws(() => releaseOptions(envOf({ MANUAL: "true" })), /candidates alone/);
    assert.throws(() => releaseOptions(envOf({ ...byHand, CHANNEL: "nightly" })), /candidates alone/);
  });
  test("refuses a prerelease not cut by hand", () => {
    assert.throws(() => releaseOptions(envOf({ CHANNEL: Channel.Prerelease })), /by hand only/);
  });
});
