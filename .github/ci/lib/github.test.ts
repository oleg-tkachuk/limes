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
  test("merges with a merge commit, only at the tested head", async () => {
    const { octokit, sent } = clientAnswering(() => ({ status: 200, body: { merged: true } }));
    await gitHubOver(octokit, REPO).merge(7, "abc");
    assert.equal(sent[0]?.method, "PUT");
    assert.match(sent[0]?.url ?? "", /\/repos\/oleg-tkachuk\/limes\/pulls\/7\/merge$/);
    assert.deepEqual(sent[0]?.body, { sha: "abc", merge_method: "merge" });
  });

  test("a refused merge throws", async () => {
    const { octokit } = clientAnswering(() => ({ status: 409, body: { message: "Head branch was modified" } }));
    await assert.rejects(gitHubOver(octokit, REPO).merge(7, "abc"));
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

  test("dispatches a release", async () => {
    const { octokit, sent } = clientAnswering(() => ({ status: 204, body: {} }));
    await gitHubOver(octokit, REPO).dispatch("release", { sha: "abc", ci_run_id: "1" });
    assert.match(sent[0]?.url ?? "", /\/dispatches$/);
    assert.deepEqual(sent[0]?.body, { event_type: "release", client_payload: { sha: "abc", ci_run_id: "1" } });
  });
});
