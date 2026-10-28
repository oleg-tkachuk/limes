// A GitHub that answers from fixed data and records what it was asked, for
// tests.

import type { ChangedFile, Comparison, GitHub, Job, NewRelease, OpenPullRequest, PullRequest, PullRequestRun, PullRequestText, RunState, WorkflowRun } from "./github.ts";

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
  return new FakeGitHub(data);
}

// FakeGitHub is a GitHub answering from data, and recording what is asked
// of it there.
class FakeGitHub implements GitHub {
  private readonly data: FakeData;

  constructor(data: FakeData) {
    this.data = data;
  }

  async pullRequest(number: number): Promise<PullRequest> {
    this.data.calls.push(`pull ${number}`);
    const pr = this.data.pulls.get(number);
    if (pr === undefined) {
      throw new Error(`no pull request ${number}`);
    }
    return pr;
  }

  async permission(login: string): Promise<string | undefined> {
    this.data.calls.push(`permission ${login}`);
    return this.data.permissions[login];
  }

  async compare(base: string, head: string): Promise<Comparison> {
    this.data.calls.push(`compare ${base}...${head}`);
    const behindBy = this.data.behindBy[head];
    if (behindBy === undefined) {
      throw new Error(`cannot compare ${head}`);
    }
    return { behindBy, files: this.data.files[head] ?? [] };
  }

  async fileAt(path: string, ref: string): Promise<string | undefined> {
    return this.data.contents[`${ref}:${path}`];
  }

  async merge(number: number, sha: string, message: string): Promise<void> {
    this.data.calls.push(`merge ${number} ${sha}`);
    if (this.data.mergeFails.has(number)) {
      throw new Error("Head branch was modified");
    }
    this.data.merged.set(number, message);
  }

  async workflowRun(runId: number): Promise<WorkflowRun> {
    const run = this.data.runs.get(runId);
    if (run === undefined) {
      throw new Error(`no run ${runId}`);
    }
    return run;
  }

  async runJobs(runId: number): Promise<Job[]> {
    return this.data.jobs.get(runId) ?? [];
  }

  async releaseExists(tag: string): Promise<boolean> {
    return this.data.released.has(tag);
  }

  async publishRelease(release: NewRelease): Promise<void> {
    if (!this.data.tags.has(release.tag)) {
      throw new Error(`tag ${release.tag} is not in the repository`);
    }
    if (!this.data.released.has(release.tag)) {
      this.data.published.push(release);
      this.data.released.add(release.tag);
    }
  }

  async sameRepoPullRequest(head: string): Promise<number | undefined> {
    return this.data.pullsFrom.get(head);
  }

  async createPullRequest(head: string, base: string, text: PullRequestText): Promise<number> {
    this.data.createdPulls.push({ head, base, text });
    const number = 100 + this.data.createdPulls.length;
    this.data.pullsFrom.set(head, number);
    return number;
  }

  async editPullRequest(number: number, text: PullRequestText): Promise<void> {
    this.data.editedPulls.push({ number, text });
  }

  async finishedRunsOf(workflow: string, branch: string, sha: string): Promise<number[]> {
    return [...this.data.runs.entries()].filter(([, r]) => r.headSha === sha && r.headBranch === branch && r.path === workflow).map(([id]) => id).reverse();
  }

  async dispatch(eventType: string, payload: Record<string, string>): Promise<void> {
    this.data.dispatched.push({ eventType, payload });
  }

  async latestRunOf(): Promise<RunState | undefined> {
    return this.data.mainRun;
  }

  async openPullRequestsInto(): Promise<OpenPullRequest[]> {
    return this.data.open;
  }

  async pullRequestRuns(_workflow: string, headSha: string): Promise<PullRequestRun[]> {
    return this.data.prRuns[headSha] ?? [];
  }

  async openIssueTitled(title: string): Promise<number | undefined> {
    return [...this.data.issues.entries()].find(([, i]) => i.title === title)?.[0];
  }

  async createIssue(title: string, body: string): Promise<number> {
    const number = 1000 + this.data.issues.size + this.data.closed.length;
    this.data.issues.set(number, { title, body, comments: [] });
    return number;
  }

  async commentOnIssue(number: number, body: string): Promise<void> {
    this.data.issues.get(number)?.comments.push(body);
  }

  async closeIssue(number: number): Promise<void> {
    this.data.issues.delete(number);
    this.data.closed.push(number);
  }
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
