import assert from "node:assert/strict";
import { describe, test } from "node:test";

import { Octokit } from "octokit";

import { authorOf, gitHubOver, parseRepo } from "./github.ts";

const REPO = { owner: "oleg-tkachuk", repo: "limes" };

// Recorded is one request the client sent.
interface Recorded {
  method: string;
  url: string;
  body: unknown;
}

// clientAnswering is an Octokit whose every request is recorded and answered
// by answer, with retries off so a failure is seen at once.
function clientAnswering(answer: (r: Recorded) => { status: number; body: unknown }): { octokit: Octokit; sent: Recorded[] } {
  const sent: Recorded[] = [];
  const fetch = async (url: string | URL, init?: RequestInit): Promise<Response> => {
    const r: Recorded = { method: init?.method ?? "GET", url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined };
    sent.push(r);
    const { status, body } = answer(r);
    // A 204 carries no body.
    return new Response(status === 204 ? null : JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
  };
  const octokit = new Octokit({ auth: "token", request: { fetch, retries: 0 }, retry: { enabled: false }, throttle: { enabled: false } });
  return { octokit, sent };
}

describe("parseRepo", () => {
  test("owner/name", () => assert.deepEqual(parseRepo("oleg-tkachuk/limes"), REPO));
  for (const bad of ["limes", "a/b/c", "a/b; rm", ""]) {
    test(`refuses ${JSON.stringify(bad)}`, () => assert.throws(() => parseRepo(bad)));
  }
});

describe("authorOf", () => {
  test("an app's REST login loses its suffix", () => assert.deepEqual(authorOf("renovate[bot]", true), { login: "renovate", bot: true }));
  test("a user keeps theirs", () => assert.deepEqual(authorOf("someone[bot]", false), { login: "someone[bot]", bot: false }));
});

describe("gitHubOver", () => {
  test("merges with a merge commit, only at the tested head, with the body given", async () => {
    const { octokit, sent } = clientAnswering(() => ({ status: 200, body: { merged: true } }));
    await gitHubOver(octokit, REPO).merge(7, "abc", "feat: what was checked");
    assert.equal(sent[0]?.method, "PUT");
    assert.match(sent[0]?.url ?? "", /\/repos\/oleg-tkachuk\/limes\/pulls\/7\/merge$/);
    assert.deepEqual(sent[0]?.body, { sha: "abc", merge_method: "merge", commit_message: "feat: what was checked" });
  });

  test("reads a commit's pull_request runs, newest first, with their pull requests' bases", async () => {
    const run = (n: number, conclusion: string | null, number: number, base: string) => ({
      id: n, run_number: n, status: conclusion === null ? "in_progress" : "completed", conclusion, display_title: `title ${n}`,
      pull_requests: [{ number, base: { ref: base } }],
    });
    const { octokit, sent } = clientAnswering(() => ({ status: 200, body: { total_count: 2, workflow_runs: [run(1, "success", 7, "main"), run(2, null, 8, "z")] } }));
    const runs = await gitHubOver(octokit, REPO).pullRequestRuns(".github/workflows/ci.yaml", "abc");
    assert.match(sent[0]?.url ?? "", /\/actions\/workflows\/ci\.yaml\/runs\?/);
    assert.match(sent[0]?.url ?? "", /event=pull_request/);
    assert.match(sent[0]?.url ?? "", /head_sha=abc/);
    assert.deepEqual(runs, [
      { status: "in_progress", conclusion: undefined, title: "title 2", pullRequests: [{ number: 8, baseRef: "z" }] },
      { status: "completed", conclusion: "success", title: "title 1", pullRequests: [{ number: 7, baseRef: "main" }] },
    ]);
  });

  test("reads a file's text at a ref, and none where the ref has none", async () => {
    const { octokit, sent } = clientAnswering((req) => (/missing/.test(req.url) ? { status: 404, body: { message: "Not Found" } } : { status: 200, body: "module x\n" }));
    const github = gitHubOver(octokit, REPO);
    assert.equal(await github.fileAt("pgstore/go.mod", "abc"), "module x\n");
    assert.match(sent[0]?.url ?? "", /\/contents\/pgstore(\/|%2F)go\.mod\?ref=abc$/);
    assert.equal(await github.fileAt("missing/go.mod", "abc"), undefined);
  });

  test("a refused merge throws", async () => {
    const { octokit } = clientAnswering(() => ({ status: 409, body: { message: "Head branch was modified" } }));
    await assert.rejects(gitHubOver(octokit, REPO).merge(7, "abc", "feat: x"));
  });

  test("an unknown collaborator has no permission", async () => {
    const { octokit } = clientAnswering(() => ({ status: 404, body: { message: "Not Found" } }));
    assert.equal(await gitHubOver(octokit, REPO).permission("stranger"), undefined);
  });

  test("another failure reading a permission throws", async () => {
    const { octokit } = clientAnswering(() => ({ status: 500, body: { message: "boom" } }));
    await assert.rejects(gitHubOver(octokit, REPO).permission("someone"));
  });

  test("compares at the head named, renames included", async () => {
    const { octokit, sent } = clientAnswering(() => ({
      status: 200,
      body: { behind_by: 2, files: [{ filename: "b.go", previous_filename: "a.go" }, { filename: "c.go" }] },
    }));
    const c = await gitHubOver(octokit, REPO).compare("main", "a9");
    assert.match(sent[0]?.url ?? "", /\/compare\/main\.\.\.a9$/);
    assert.deepEqual(c, {
      behindBy: 2,
      files: [
        { filename: "b.go", previousFilename: "a.go" },
        { filename: "c.go", previousFilename: undefined },
      ],
    });
  });

  test("reads a pull request, an app's author by its GraphQL login", async () => {
    const { octokit } = clientAnswering(() => ({
      status: 200,
      body: {
        number: 7,
        state: "open",
        draft: false,
        user: { login: "renovate[bot]", type: "Bot" },
        base: { ref: "main", repo: { full_name: "oleg-tkachuk/limes" } },
        head: { sha: "abc", repo: { full_name: "someone/limes" } },
        changed_files: 4,
      },
    }));
    assert.deepEqual(await gitHubOver(octokit, REPO).pullRequest(7), {
      number: 7,
      open: true,
      draft: false,
      fromFork: true,
      author: { login: "renovate", bot: true },
      baseRef: "main",
      headSha: "abc",
      changedFiles: 4,
    });
  });

  test("a nested module's tag reaches the API escaped, as GitHub reads it", async () => {
    const { octokit, sent } = clientAnswering((r) =>
      r.url.includes("/releases/tags/") ? { status: 404, body: { message: "Not Found" } } : { status: 200, body: { body: "list" } },
    );
    const github = gitHubOver(octokit, REPO);
    assert.equal(await github.releaseExists("pgstore/v0.29.2"), false);
    assert.match(sent[0]?.url ?? "", /\/releases\/tags\/pgstore%2Fv0\.29\.2$/);
    await github.publishRelease({ tag: "pgstore/v0.30.0", notes: "", previousTag: "pgstore/v0.29.2", latest: false });
    assert.match(sent[1]?.url ?? "", /\/git\/ref\/tags%2Fpgstore%2Fv0\.30\.0$/);
    assert.deepEqual(sent[2]?.body, { tag_name: "pgstore/v0.30.0", previous_tag_name: "pgstore/v0.29.2" });
    assert.deepEqual(sent[3]?.body, { tag_name: "pgstore/v0.30.0", name: "pgstore/v0.30.0", body: "list", make_latest: "false" });
  });

  test("a published release's body is the notes, then GitHub's list", async () => {
    const { octokit, sent } = clientAnswering(() => ({ status: 200, body: { body: "list" } }));
    await gitHubOver(octokit, REPO).publishRelease({ tag: "v1.2.3", notes: "notes\n", previousTag: undefined, latest: true });
    assert.deepEqual(sent[2]?.body, { tag_name: "v1.2.3", name: "v1.2.3", body: "notes\n\nlist" });
  });

  test("a prerelease is never the latest release", async () => {
    const { octokit, sent } = clientAnswering(() => ({ status: 200, body: { body: "list" } }));
    await gitHubOver(octokit, REPO).publishRelease({ tag: "v1.0.0-rc.1", notes: "", previousTag: undefined, latest: true, prerelease: true });
    assert.deepEqual(sent[2]?.body, { tag_name: "v1.0.0-rc.1", name: "v1.0.0-rc.1", body: "list", make_latest: "false", prerelease: true });
  });

  test("a release of a tag not pushed is refused before anything is written", async () => {
    const { octokit, sent } = clientAnswering(() => ({ status: 404, body: { message: "Not Found" } }));
    await assert.rejects(gitHubOver(octokit, REPO).publishRelease({ tag: "v9.9.9", notes: "", previousTag: undefined, latest: true }), /not in the repository/);
    assert.equal(sent.length, 1);
  });

  test("a release another run published meanwhile is no failure; one still missing is", async () => {
    for (const [published, fails] of [[true, false], [false, true]] as const) {
      const { octokit } = clientAnswering((r) =>
        r.method === "POST" && r.url.endsWith("/releases")
          ? { status: 422, body: { message: "already_exists" } }
          : r.url.includes("/releases/tags/") && !published
            ? { status: 404, body: { message: "Not Found" } }
            : { status: 200, body: { body: "list" } },
      );
      const publish = gitHubOver(octokit, REPO).publishRelease({ tag: "v1.2.3", notes: "", previousTag: undefined, latest: true });
      if (fails) {
        await assert.rejects(publish);
      } else {
        await publish;
      }
    }
  });

  test("lists the open pull requests into a base, neither drafts nor forks", async () => {
    const pull = (number: number, draft: boolean | null, fullName: string | null) => ({
      number, draft, head: { sha: `sha${number}`, repo: fullName === null ? null : { full_name: fullName } },
    });
    const { octokit, sent } = clientAnswering(() => ({
      status: 200,
      body: [pull(1, false, "oleg-tkachuk/limes"), pull(2, true, "oleg-tkachuk/limes"), pull(3, null, "fork/limes"), pull(4, null, null), pull(5, null, "oleg-tkachuk/limes")],
    }));
    const pulls = await gitHubOver(octokit, REPO).openPullRequestsInto("main");
    assert.match(sent[0]?.url ?? "", /\/pulls\?.*base=main/);
    assert.deepEqual(pulls, [{ number: 1, headSha: "sha1" }, { number: 5, headSha: "sha5" }]);
  });

  test("reads a branch's latest finished push run, or none", async () => {
    const run = { id: 9, conclusion: null, head_sha: "abc", html_url: "https://example.com/runs/9" };
    for (const [runs, want] of [[[run], { id: 9, conclusion: "", headSha: "abc", url: "https://example.com/runs/9" }], [[], undefined]] as const) {
      const { octokit, sent } = clientAnswering(() => ({ status: 200, body: { total_count: runs.length, workflow_runs: runs } }));
      assert.deepEqual(await gitHubOver(octokit, REPO).latestRunOf(".github/workflows/ci.yaml", "main"), want);
      assert.match(sent[0]?.url ?? "", /\/actions\/workflows\/ci\.yaml\/runs\?.*branch=main/);
    }
  });

  test("dispatches a release", async () => {
    const { octokit, sent } = clientAnswering(() => ({ status: 204, body: {} }));
    await gitHubOver(octokit, REPO).dispatch("release", { sha: "abc", ci_run_id: "1" });
    assert.match(sent[0]?.url ?? "", /\/dispatches$/);
    assert.deepEqual(sent[0]?.body, { event_type: "release", client_payload: { sha: "abc", ci_run_id: "1" } });
  });
});
