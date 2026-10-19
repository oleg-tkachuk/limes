// A GitHub that answers from fixed data and records what it was asked, for
// tests.

import type { ChangedFile, GitHub, Job, NewRelease, PullRequest, PullRequestText, QueuedPullRequest, WorkflowRun } from "./github.ts";

// FakeGitHub is the data a FakeGitHub answers with, and its log.
export interface FakeData {
  pulls: Map<number, PullRequest>;
  queue: QueuedPullRequest[];
  // permissions by login; a login missing is unknown to the API.
  permissions: Record<string, string>;
  // files and behindBy by head sha; a sha missing fails the comparison.
  files: Record<string, ChangedFile[]>;
  behindBy: Record<string, number>;
  // mergeFails and rebaseFails name the pull requests whose merge or rebase
  // GitHub refuses.
  mergeFails: Set<number>;
  rebaseFails: Set<number>;
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
  calls: string[];
}

export function fakeData(): FakeData {
  return {
    pulls: new Map(),
    queue: [],
    permissions: {},
    files: {},
    behindBy: {},
    mergeFails: new Set(),
    rebaseFails: new Set(),
    runs: new Map(),
    jobs: new Map(),
    released: new Set(),
    published: [],
    tags: new Set(),
    pullsFrom: new Map(),
    createdPulls: [],
    editedPulls: [],
    dispatched: [],
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
    async openPullRequests(base, limit) {
      data.calls.push(`list ${base} ${limit}`);
      return data.queue;
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
    async merge(number, sha) {
      data.calls.push(`merge ${number} ${sha}`);
      if (data.mergeFails.has(number)) {
        throw new Error("Head branch was modified");
      }
    },
    async rebaseBranch(number, sha) {
      data.calls.push(`rebase ${number} ${sha}`);
      if (data.rebaseFails.has(number)) {
        throw new Error("merge conflict");
      }
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
    async passedRunsOf(workflow, branch, sha) {
      return [...data.runs.entries()].filter(([, r]) => r.headSha === sha && r.headBranch === branch && r.path === workflow).map(([id]) => id).reverse();
    },
    async dispatch(eventType, payload) {
      data.dispatched.push({ eventType, payload });
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
