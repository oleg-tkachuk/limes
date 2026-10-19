import assert from "node:assert/strict";
import { describe, test } from "node:test";

import { Octokit } from "octokit";

import { authorOf, checkRunOf, gitHubOver, parseRepo, queuedOf, type GraphQLPullRequest } from "./github.ts";

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
    return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
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

describe("checkRunOf", () => {
  test("a check run", () =>
    assert.deepEqual(checkRunOf({ __typename: "CheckRun", name: "All checks passed", conclusion: "SUCCESS", startedAt: "t" }), {
      name: "All checks passed",
      conclusion: "SUCCESS",
      startedAt: "t",
    }));
  test("a check run still going", () =>
    assert.equal(checkRunOf({ __typename: "CheckRun", name: "x", conclusion: null, startedAt: null }).conclusion, undefined));
  test("a pending status", () => assert.equal(checkRunOf({ __typename: "StatusContext", context: "x", state: "PENDING" }).conclusion, undefined));
  test("a failed status", () => assert.equal(checkRunOf({ __typename: "StatusContext", context: "x", state: "FAILURE" }).conclusion, "FAILURE"));
});

test("queuedOf reads a listed pull request", () => {
  const node: GraphQLPullRequest = {
    number: 9,
    state: "OPEN",
    isDraft: false,
    isCrossRepository: false,
    mergeStateStatus: "BEHIND",
    baseRefName: "main",
    headRefOid: "a9",
    changedFiles: 3,
    author: { login: "renovate", __typename: "Bot" },
    autoMergeRequest: { enabledAt: "t" },
    commits: { nodes: [{ commit: { statusCheckRollup: { contexts: { nodes: [{ __typename: "CheckRun", name: "c", conclusion: "SUCCESS" }] } } } }] },
  };
  const pr = queuedOf(node);
  assert.deepEqual(pr.author, { login: "renovate", bot: true });
  assert.equal(pr.autoMerge, true);
  assert.equal(pr.changedFiles, 3);
  assert.deepEqual(pr.checks, [{ name: "c", conclusion: "SUCCESS", startedAt: undefined }]);
  assert.deepEqual(queuedOf({ ...node, changedFiles: null, autoMergeRequest: null, commits: { nodes: [] } }).checks, []);
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

  test("rebases only at the compared head", async () => {
    const { octokit, sent } = clientAnswering(() => ({ status: 202, body: {} }));
    await gitHubOver(octokit, REPO).rebaseBranch(9, "a9");
    assert.match(sent[0]?.url ?? "", /\/pulls\/9\/update-branch$/);
    assert.deepEqual(sent[0]?.body, { expected_head_sha: "a9", update_method: "rebase" });
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

  test("lists the open pull requests into a branch", async () => {
    const { octokit, sent } = clientAnswering(() => ({ status: 200, body: { data: { repository: { pullRequests: { nodes: [] } } } } }));
    assert.deepEqual(await gitHubOver(octokit, REPO).openPullRequests("main", 200), []);
    const variables = (sent[0]?.body as { variables: Record<string, unknown> }).variables;
    assert.deepEqual({ ...variables, checks: undefined }, { ...REPO, base: "main", limit: 200, checks: undefined });
  });
});
