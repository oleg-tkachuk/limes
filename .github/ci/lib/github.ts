// What the commands read from and do to GitHub, behind a narrow interface
// they are tested against, and its implementation over Octokit, GitHub's own
// client, whose retry and throttling plugins answer a transient failure or a
// rate limit.

import { Octokit } from "octokit";

// Repo is a repository as owner and name.
export interface Repo {
  owner: string;
  repo: string;
}

// parseRepo reads owner/name, as GITHUB_REPOSITORY spells it.
export function parseRepo(fullName: string): Repo {
  const match = /^([\w.-]+)\/([\w.-]+)$/.exec(fullName);
  if (match === null || match[1] === undefined || match[2] === undefined) {
    throw new Error(`${fullName} is not owner/name`);
  }
  return { owner: match[1], repo: match[2] };
}

// Author is who opened a pull request: a user, or an app's bot.
export interface Author {
  login: string;
  bot: boolean;
}

// PullRequest is what the merge automation reads of one pull request.
export interface PullRequest {
  number: number;
  open: boolean;
  draft: boolean;
  // fromFork: the head is in another repository than the base.
  fromFork: boolean;
  author: Author;
  baseRef: string;
  headSha: string;
  // changedFiles is GitHub's count of the files the pull request changes;
  // undefined when it gave none.
  changedFiles: number | undefined;
}

// CheckRun is one run of a check, or one commit status, at a pull request's
// head.
export interface CheckRun {
  name: string;
  // conclusion is a finished run's outcome, upper case as GitHub's GraphQL
  // API spells it; undefined while the run is going.
  conclusion: string | undefined;
  startedAt: string | undefined;
}

// QueuedPullRequest is an open pull request as the merge queue reads it.
export interface QueuedPullRequest extends PullRequest {
  // mergeState is GitHub's mergeStateStatus: DIRTY for a conflict.
  mergeState: string;
  autoMerge: boolean;
  checks: CheckRun[];
}

// ChangedFile is a file a comparison lists, with the path it was renamed
// from, if any.
export interface ChangedFile {
  filename: string;
  previousFilename: string | undefined;
}

// Comparison is a commit compared with a branch: how far it is behind it and
// the files it changes since their merge base.
export interface Comparison {
  behindBy: number;
  files: ChangedFile[];
}

// WorkflowRun is what the release reads of a workflow run.
export interface WorkflowRun {
  path: string;
  event: string;
  headBranch: string;
  headSha: string;
  repository: string;
}

// Job is one job of a workflow run; conclusion is undefined until it ends.
export interface Job {
  name: string;
  status: string;
  conclusion: string | undefined;
}

// NewRelease is a GitHub release to publish for a tag already pushed.
export interface NewRelease {
  tag: string;
  // notes come first in the body, before GitHub's generated list of the
  // pull requests merged since previousTag.
  notes: string;
  previousTag: string | undefined;
  // latest marks it Latest; otherwise GitHub is told it is not.
  latest: boolean;
}

// PullRequestText is a pull request's title and body.
export interface PullRequestText {
  title: string;
  body: string;
}

// GitHub is everything the commands ask of the API.
export interface GitHub {
  pullRequest(number: number): Promise<PullRequest>;
  openPullRequests(base: string, limit: number): Promise<QueuedPullRequest[]>;
  // permission is a login's permission on the repository — admin, write,
  // read or none — or undefined when the API does not know it as a
  // collaborator.
  permission(login: string): Promise<string | undefined>;
  compare(base: string, head: string): Promise<Comparison>;
  // merge merges a pull request with a merge commit, refused unless its
  // head is sha.
  merge(number: number, sha: string): Promise<void>;
  // rebaseBranch rebases a pull request's branch onto its base, refused
  // unless its head is expectedHeadSha.
  rebaseBranch(number: number, expectedHeadSha: string): Promise<void>;
  workflowRun(runId: number): Promise<WorkflowRun>;
  runJobs(runId: number): Promise<Job[]>;
  releaseExists(tag: string): Promise<boolean>;
  // publishRelease publishes a release of a tag the repository holds; one
  // published already, by a call whose answer was lost, stands.
  publishRelease(release: NewRelease): Promise<void>;
  // sameRepoPullRequest is the open pull request from this repository's
  // branch head into base, if any: a fork's of the same branch name is not.
  sameRepoPullRequest(head: string, base: string): Promise<number | undefined>;
  createPullRequest(head: string, base: string, text: PullRequestText): Promise<number>;
  editPullRequest(number: number, text: PullRequestText): Promise<void>;
  // dispatch sends a repository_dispatch event of eventType with payload.
  dispatch(eventType: string, payload: Record<string, string>): Promise<void>;
}

// MERGE_METHOD is how the automation merges: a merge commit, whose body is
// the pull request's title the release reads.
const MERGE_METHOD = "merge";
// UPDATE_METHOD is how a branch behind its base is brought up to it.
const UPDATE_METHOD = "rebase";
// NOT_FOUND is the status of an unknown collaborator.
const NOT_FOUND = 404;
// CHECKS_PER_HEAD bounds the check runs and statuses read at a head.
const CHECKS_PER_HEAD = 100;
// PAGE_SIZE is the most nodes the GraphQL API returns for one connection
// page; a longer listing is read a page at a time.
export const PAGE_SIZE = 100;

// OPEN_PULL_REQUESTS reads the open pull requests into a branch with what
// the merge queue decides on. `gh pr list` read the same fields.
const OPEN_PULL_REQUESTS = `
query ($owner: String!, $repo: String!, $base: String!, $first: Int!, $after: String, $checks: Int!) {
  repository(owner: $owner, name: $repo) {
    pullRequests(states: OPEN, baseRefName: $base, first: $first, after: $after, orderBy: {field: CREATED_AT, direction: ASC}) {
      pageInfo { hasNextPage endCursor }
      nodes {
        number
        state
        isDraft
        isCrossRepository
        mergeStateStatus
        baseRefName
        headRefOid
        changedFiles
        author { login __typename }
        autoMergeRequest { enabledAt }
        commits(last: 1) {
          nodes {
            commit {
              statusCheckRollup {
                contexts(first: $checks) {
                  nodes {
                    __typename
                    ... on CheckRun { name conclusion startedAt }
                    ... on StatusContext { context state createdAt }
                  }
                }
              }
            }
          }
        }
      }
    }
  }
}`;

// GraphQL types of the query above, as far as it is read.
interface GraphQLContext {
  __typename: string;
  name?: string;
  conclusion?: string | null;
  startedAt?: string | null;
  context?: string;
  state?: string;
  createdAt?: string | null;
}

export interface GraphQLPullRequest {
  number: number;
  state: string;
  isDraft: boolean;
  isCrossRepository: boolean;
  mergeStateStatus: string;
  baseRefName: string;
  headRefOid: string;
  changedFiles: number | null;
  author: { login: string; __typename: string } | null;
  autoMergeRequest: { enabledAt: string } | null;
  commits: { nodes: { commit: { statusCheckRollup: { contexts: { nodes: GraphQLContext[] } } | null } }[] };
}

// PullRequestPage is one page of the query above.
interface PullRequestPage {
  repository: { pullRequests: { pageInfo: { hasNextPage: boolean; endCursor: string | null }; nodes: GraphQLPullRequest[] } };
}

// GRAPHQL_OPEN and GRAPHQL_BOT are how the GraphQL API spells an open pull
// request and an app's author; PENDING, a commit status still going.
const GRAPHQL_OPEN = "OPEN";
const GRAPHQL_BOT = "Bot";
const STATUS_PENDING = "PENDING";

// checkRunOf reads one context of a status check rollup: a check run, or a
// commit status, whose state stands for a conclusion once it is not pending.
export function checkRunOf(node: GraphQLContext): CheckRun {
  if (node.__typename === "StatusContext") {
    return {
      name: node.context ?? "",
      conclusion: node.state === STATUS_PENDING ? undefined : (node.state ?? undefined),
      startedAt: node.createdAt ?? undefined,
    };
  }
  return { name: node.name ?? "", conclusion: node.conclusion ?? undefined, startedAt: node.startedAt ?? undefined };
}

// queuedOf reads one pull request of the query above.
export function queuedOf(node: GraphQLPullRequest): QueuedPullRequest {
  const rollup = node.commits.nodes[0]?.commit.statusCheckRollup;
  return {
    number: node.number,
    open: node.state === GRAPHQL_OPEN,
    draft: node.isDraft,
    fromFork: node.isCrossRepository,
    author: authorOf(node.author?.login ?? "", node.author?.__typename === GRAPHQL_BOT),
    baseRef: node.baseRefName,
    headSha: node.headRefOid,
    changedFiles: node.changedFiles ?? undefined,
    mergeState: node.mergeStateStatus,
    autoMerge: node.autoMergeRequest !== null,
    checks: (rollup?.contexts.nodes ?? []).map(checkRunOf),
  };
}

// BOT_SUFFIX ends an app's login in the REST API, which the GraphQL API
// leaves off.
const BOT_SUFFIX = "[bot]";

// authorOf is an author as the GraphQL API names it, whichever API read it.
export function authorOf(login: string, bot: boolean): Author {
  return { login: bot && login.endsWith(BOT_SUFFIX) ? login.slice(0, -BOT_SUFFIX.length) : login, bot };
}

// statusOf is an Octokit error's HTTP status, if it has one.
function statusOf(err: unknown): number | undefined {
  return typeof err === "object" && err !== null && "status" in err && typeof err.status === "number" ? err.status : undefined;
}

// gitHubOver is the GitHub of repo through octokit.
export function gitHubOver(octokit: Octokit, repo: Repo): GitHub {
  return {
    async pullRequest(number) {
      const { data } = await octokit.rest.pulls.get({ ...repo, pull_number: number });
      return {
        number: data.number,
        open: data.state === "open",
        draft: data.draft ?? false,
        fromFork: data.head.repo?.full_name !== data.base.repo.full_name,
        author: authorOf(data.user.login, data.user.type === GRAPHQL_BOT),
        baseRef: data.base.ref,
        headSha: data.head.sha,
        changedFiles: data.changed_files,
      };
    },
    async openPullRequests(base, limit) {
      const listed: QueuedPullRequest[] = [];
      let after: string | null = null;
      while (listed.length < limit) {
        const page: PullRequestPage = await octokit.graphql<PullRequestPage>(OPEN_PULL_REQUESTS, {
          ...repo,
          base,
          first: Math.min(PAGE_SIZE, limit - listed.length),
          after,
          checks: CHECKS_PER_HEAD,
        });
        const { nodes, pageInfo } = page.repository.pullRequests;
        listed.push(...nodes.map(queuedOf));
        // An empty page ends the listing whatever it says follows it.
        if (!pageInfo.hasNextPage || nodes.length === 0) {
          break;
        }
        after = pageInfo.endCursor;
      }
      return listed;
    },
    async permission(login) {
      try {
        const { data } = await octokit.rest.repos.getCollaboratorPermissionLevel({ ...repo, username: login });
        return data.permission;
      } catch (err) {
        if (statusOf(err) === NOT_FOUND) {
          return undefined;
        }
        throw err;
      }
    },
    async compare(base, head) {
      const { data } = await octokit.rest.repos.compareCommitsWithBasehead({ ...repo, basehead: `${base}...${head}` });
      return {
        behindBy: data.behind_by,
        files: (data.files ?? []).map((f) => ({ filename: f.filename, previousFilename: f.previous_filename })),
      };
    },
    async merge(number, sha) {
      await octokit.rest.pulls.merge({ ...repo, pull_number: number, sha, merge_method: MERGE_METHOD });
    },
    async rebaseBranch(number, expectedHeadSha) {
      // update_method is newer than the published route types.
      const params = { ...repo, pull_number: number, expected_head_sha: expectedHeadSha, update_method: UPDATE_METHOD };
      await octokit.request("PUT /repos/{owner}/{repo}/pulls/{pull_number}/update-branch", params);
    },
    async workflowRun(runId) {
      const { data } = await octokit.rest.actions.getWorkflowRun({ ...repo, run_id: runId });
      return {
        path: data.path,
        event: data.event,
        headBranch: data.head_branch ?? "",
        headSha: data.head_sha,
        repository: data.repository.full_name,
      };
    },
    async runJobs(runId) {
      const jobs = await octokit.paginate(octokit.rest.actions.listJobsForWorkflowRun, { ...repo, run_id: runId, per_page: PAGE_SIZE });
      return jobs.map((j) => ({ name: j.name, status: j.status, conclusion: j.conclusion ?? undefined }));
    },
    async releaseExists(tag) {
      return existsBy(() => octokit.rest.repos.getReleaseByTag({ ...repo, tag }));
    },
    async publishRelease(release) {
      // The tag must be pushed already: a release of a missing tag would
      // create it at the default branch's head.
      if (!(await existsBy(() => octokit.rest.git.getRef({ ...repo, ref: `tags/${release.tag}` })))) {
        throw new Error(`tag ${release.tag} is not in the repository`);
      }
      const { data: generated } = await octokit.rest.repos.generateReleaseNotes({
        ...repo,
        tag_name: release.tag,
        ...(release.previousTag === undefined ? {} : { previous_tag_name: release.previousTag }),
      });
      const body = release.notes === "" ? generated.body : `${release.notes}\n${generated.body}`;
      try {
        await octokit.rest.repos.createRelease({
          ...repo,
          tag_name: release.tag,
          name: release.tag,
          body,
          ...(release.latest ? {} : { make_latest: MAKE_LATEST_FALSE }),
        });
      } catch (err) {
        if (!(await this.releaseExists(release.tag))) {
          throw err;
        }
      }
    },
    async sameRepoPullRequest(head, base) {
      const { data } = await octokit.rest.pulls.list({ ...repo, head: `${repo.owner}:${head}`, base, state: "open" });
      return data.find((p) => p.head.repo?.full_name === `${repo.owner}/${repo.repo}`)?.number;
    },
    async createPullRequest(head, base, text) {
      const { data } = await octokit.rest.pulls.create({ ...repo, head, base, title: text.title, body: text.body });
      return data.number;
    },
    async editPullRequest(number, text) {
      await octokit.rest.pulls.update({ ...repo, pull_number: number, title: text.title, body: text.body });
    },
    async dispatch(eventType, payload) {
      await octokit.rest.repos.createDispatchEvent({ ...repo, event_type: eventType, client_payload: payload });
    },
  };
}

// existsBy reports whether lookup finds what it looks for: a 404 is no,
// any other failure is thrown.
async function existsBy(lookup: () => Promise<unknown>): Promise<boolean> {
  try {
    await lookup();
    return true;
  } catch (err) {
    if (statusOf(err) === NOT_FOUND) {
      return false;
    }
    throw err;
  }
}

// MAKE_LATEST_FALSE tells GitHub a release is not the Latest.
const MAKE_LATEST_FALSE = "false";

// gitHubFor is the GitHub of the repository fullName, owner/name, acting
// with token.
export function gitHubFor(token: string, fullName: string): GitHub {
  return gitHubOver(new Octokit({ auth: token }), parseRepo(fullName));
}
