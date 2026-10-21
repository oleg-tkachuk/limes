// A GitHub that answers from fixed data and records what it was asked, for
// tests.

import type { ChangedFile, GitHub, Job, NewRelease, OpenPullRequest, PullRequest, PullRequestRun, PullRequestText, RunState, WorkflowRun } from "./github.ts";

// FakeGitHub is the data a FakeGitHub answers with, and its log.
export interface FakeData {
  pulls: Map<number, PullRequest>;
  // permissions by login; a login missing is unknown to the API.
  permissions: Record<string, string>;
  // files and behindBy by head sha; a sha missing fails the comparison.
  files: Record<string, ChangedFile[]>;
  behindBy: Record<string, number>;
  // contents are file texts by `ref:path`; one missing is not in that ref.
  contents: Record<string, string>;
  // mergeFails names the pull requests whose merge GitHub refuses.
  mergeFails: Set<number>;
  // runs and jobs by run id; a run missing is not found.
  runs: Map<number, WorkflowRun>;
  jobs: Map<number, Job[]>;
  // released are the tags with a GitHub release; published, the releases
  // this fake was asked to publish; tags, the tags the repository holds.
  released: Set<string>;
  published: NewRelease[];
  tags: Set<string>;
  // pullsFrom are the open same-repository pull requests by head branch;
  // created and edited log what was asked.
  pullsFrom: Map<string, number>;
  createdPulls: { head: string; base: string; text: PullRequestText }[];
  editedPulls: { number: number; text: PullRequestText }[];
  dispatched: { eventType: string; payload: Record<string, string> }[];
  // mainRun is the newest finished run on main; open, the open pull
  // requests; prRuns, the pull_request runs of CI by head sha, newest first;
  // merged, the merge commit body each merged pull request got.
  mainRun: RunState | undefined;
  open: OpenPullRequest[];
  prRuns: Record<string, PullRequestRun[]>;
  merged: Map<number, string>;
  // issues are the open issues by number, with their comments.
  issues: Map<number, { title: string; body: string; comments: string[] }>;
  closed: number[];
  calls: string[];
}

export function fakeData(): FakeData {
  return {
    pulls: new Map(),
    permissions: {},
    files: {},
    behindBy: {},
    contents: {},
    mergeFails: new Set(),
    runs: new Map(),
    jobs: new Map(),
    released: new Set(),
    published: [],
    tags: new Set(),
    pullsFrom: new Map(),
    createdPulls: [],
    editedPulls: [],
    dispatched: [],
    mainRun: undefined,
    open: [],
    prRuns: {},
    merged: new Map(),
    issues: new Map(),
    closed: [],
    calls: [],
  };
}

// fakeGitHub answers from data.
export function fakeGitHub(data: FakeData): GitHub {
  return {
    async pullRequest(number) {
      data.calls.push(`pull ${number}`);
      const pr = data.pulls.get(number);
      if (pr === undefined) {
        throw new Error(`no pull request ${number}`);
      }
      return pr;
    },
    async permission(login) {
      data.calls.push(`permission ${login}`);
      return data.permissions[login];
    },
    async compare(base, head) {
      data.calls.push(`compare ${base}...${head}`);
      const behindBy = data.behindBy[head];
      if (behindBy === undefined) {
        throw new Error(`cannot compare ${head}`);
      }
      return { behindBy, files: data.files[head] ?? [] };
    },
    async fileAt(path, ref) {
      return data.contents[`${ref}:${path}`];
    },
    async merge(number, sha, message) {
      data.calls.push(`merge ${number} ${sha}`);
      if (data.mergeFails.has(number)) {
        throw new Error("Head branch was modified");
      }
      data.merged.set(number, message);
    },
    async workflowRun(runId) {
      const run = data.runs.get(runId);
      if (run === undefined) {
        throw new Error(`no run ${runId}`);
      }
      return run;
    },
    async runJobs(runId) {
      return data.jobs.get(runId) ?? [];
    },
    async releaseExists(tag) {
      return data.released.has(tag);
    },
    async publishRelease(release) {
      if (!data.tags.has(release.tag)) {
        throw new Error(`tag ${release.tag} is not in the repository`);
      }
      if (!data.released.has(release.tag)) {
        data.published.push(release);
        data.released.add(release.tag);
      }
    },
    async sameRepoPullRequest(head) {
      return data.pullsFrom.get(head);
    },
    async createPullRequest(head, base, text) {
      data.createdPulls.push({ head, base, text });
      const number = 100 + data.createdPulls.length;
      data.pullsFrom.set(head, number);
      return number;
    },
    async editPullRequest(number, text) {
      data.editedPulls.push({ number, text });
    },
    async finishedRunsOf(workflow, branch, sha) {
      return [...data.runs.entries()].filter(([, r]) => r.headSha === sha && r.headBranch === branch && r.path === workflow).map(([id]) => id).reverse();
    },
    async dispatch(eventType, payload) {
      data.dispatched.push({ eventType, payload });
    },
    async latestRunOf() {
      return data.mainRun;
    },
    async openPullRequestsInto() {
      return data.open;
    },
    async pullRequestRuns(_workflow, headSha) {
      return data.prRuns[headSha] ?? [];
    },
    async openIssueTitled(title) {
      return [...data.issues.entries()].find(([, i]) => i.title === title)?.[0];
    },
    async createIssue(title, body) {
      const number = 1000 + data.issues.size + data.closed.length;
      data.issues.set(number, { title, body, comments: [] });
      return number;
    },
    async commentOnIssue(number, body) {
      data.issues.get(number)?.comments.push(body);
    },
    async closeIssue(number) {
      data.issues.delete(number);
      data.closed.push(number);
    },
  };
}

// changed reads paths as a listing: `old>new` is a rename.
export function changed(...paths: string[]): ChangedFile[] {
  return paths.map((p) => {
    const [from, to] = p.split(">");
    return to === undefined ? { filename: p, previousFilename: undefined } : { filename: to, previousFilename: from };
  });
}

// PULL_REQUEST_TITLE is the title a fake pull_request run ran under.
export const PULL_REQUEST_TITLE = "feat: a change";

// RUNNING stands for a run that has not ended; SUCCESS_CONCLUSION, one that
// passed.
export const RUNNING = "running";
const SUCCESS_CONCLUSION = "success";

// prRun is a pull_request run of CI for pull request number into baseRef,
// ended as conclusion, or still going for RUNNING.
export function prRun(number: number, conclusion: string = SUCCESS_CONCLUSION, baseRef = "main", title = PULL_REQUEST_TITLE): PullRequestRun {
  const running = conclusion === RUNNING;
  return { status: running ? "in_progress" : "completed", conclusion: running ? undefined : conclusion, title, pullRequests: [{ number, baseRef }] };
}
