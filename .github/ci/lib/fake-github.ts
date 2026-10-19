// A GitHub that answers from fixed data and records what it was asked, for
// tests.

import type { ChangedFile, GitHub, PullRequest, QueuedPullRequest } from "./github.ts";

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
  };
}

// changed reads paths as a listing: `old>new` is a rename.
export function changed(...paths: string[]): ChangedFile[] {
  return paths.map((p) => {
    const [from, to] = p.split(">");
    return to === undefined ? { filename: p, previousFilename: undefined } : { filename: to, previousFilename: from };
  });
}
