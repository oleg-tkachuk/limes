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
  // prerelease marks a release candidate, which is never the Latest.
  prerelease?: boolean;
}

// PullRequestText is a pull request's title and body.
export interface PullRequestText {
  title: string;
  body: string;
}

// RunState is the newest finished run of a workflow: how it ended, the
// commit it ran for, and where to read it.
export interface RunState {
  conclusion: string;
  headSha: string;
  url: string;
}

// OpenPullRequest is an open pull request's number and head.
export interface OpenPullRequest {
  number: number;
  headSha: string;
}

// GitHub is everything the commands ask of the API.
export interface GitHub {
  pullRequest(number: number): Promise<PullRequest>;
  // permission is a login's permission on the repository — admin, write,
  // read or none — or undefined when the API does not know it as a
  // collaborator.
  permission(login: string): Promise<string | undefined>;
  compare(base: string, head: string): Promise<Comparison>;
  // merge merges a pull request with a merge commit, refused unless its
  // head is sha.
  merge(number: number, sha: string): Promise<void>;
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
  // passedRunsOf is the ids of the completed, successful push runs of the
  // workflow file on branch that tested sha, newest first.
  passedRunsOf(workflow: string, branch: string, sha: string): Promise<number[]>;
  // latestRunOf is the newest finished push run of the workflow file on
  // branch, if any.
  latestRunOf(workflow: string, branch: string): Promise<RunState | undefined>;
  // openPullRequestsInto is every open, ready pull request from this
  // repository into base.
  openPullRequestsInto(base: string): Promise<OpenPullRequest[]>;
  // checkConclusion is how the newest run of the check named name at sha
  // ended; undefined while it runs or when it has not.
  checkConclusion(sha: string, name: string): Promise<string | undefined>;
  // openIssueTitled is the number of the open issue titled title, if any.
  openIssueTitled(title: string): Promise<number | undefined>;
  createIssue(title: string, body: string): Promise<number>;
  commentOnIssue(number: number, body: string): Promise<void>;
  closeIssue(number: number): Promise<void>;
  // dispatch sends a repository_dispatch event of eventType with payload.
  dispatch(eventType: string, payload: Record<string, string>): Promise<void>;
}

// MERGE_METHOD is how the automation merges: a merge commit, whose body is
// the pull request's title the release reads.
const MERGE_METHOD = "merge";
// NOT_FOUND is the status of an unknown collaborator.
const NOT_FOUND = 404;
// PAGE_SIZE is the most items the API returns in one page.
export const PAGE_SIZE = 100;

// BOT_TYPE is how the API types an app's account.
const BOT_TYPE = "Bot";

// BOT_SUFFIX ends an app's login in the REST API.
const BOT_SUFFIX = "[bot]";

// authorOf is an author with an app's login as the app's own name, without
// the REST API's suffix.
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
        author: authorOf(data.user.login, data.user.type === BOT_TYPE),
        baseRef: data.base.ref,
        headSha: data.head.sha,
        changedFiles: data.changed_files,
      };
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
          ...(release.latest && release.prerelease !== true ? {} : { make_latest: MAKE_LATEST_FALSE }),
          ...(release.prerelease === true ? { prerelease: true } : {}),
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
    async passedRunsOf(workflow, branch, sha) {
      const { data } = await octokit.rest.actions.listWorkflowRuns({
        ...repo,
        workflow_id: workflow.split("/").at(-1) ?? workflow,
        branch,
        event: "push",
        status: "success",
        head_sha: sha,
        per_page: PAGE_SIZE,
      });
      return data.workflow_runs.sort((a, b) => b.run_number - a.run_number).map((r) => r.id);
    },
    async latestRunOf(workflow, branch) {
      const { data } = await octokit.rest.actions.listWorkflowRuns({
        ...repo,
        workflow_id: workflow.split("/").at(-1) ?? workflow,
        branch,
        event: "push",
        status: "completed",
        per_page: 1,
      });
      const run = data.workflow_runs[0];
      return run === undefined ? undefined : { conclusion: run.conclusion ?? "", headSha: run.head_sha, url: run.html_url };
    },
    async openPullRequestsInto(base) {
      const pulls = await octokit.paginate(octokit.rest.pulls.list, { ...repo, base, state: "open", per_page: PAGE_SIZE });
      return pulls
        .filter((p) => !(p.draft ?? false) && p.head.repo?.full_name === `${repo.owner}/${repo.repo}`)
        .map((p) => ({ number: p.number, headSha: p.head.sha }));
    },
    async checkConclusion(sha, name) {
      const { data } = await octokit.rest.checks.listForRef({ ...repo, ref: sha, check_name: name, filter: "latest" });
      return data.check_runs[0]?.conclusion ?? undefined;
    },
    async openIssueTitled(title) {
      const issues = await octokit.paginate(octokit.rest.issues.listForRepo, { ...repo, state: "open", per_page: PAGE_SIZE });
      return issues.find((i) => i.pull_request === undefined && i.title === title)?.number;
    },
    async createIssue(title, body) {
      const { data } = await octokit.rest.issues.create({ ...repo, title, body });
      return data.number;
    },
    async commentOnIssue(number, body) {
      await octokit.rest.issues.createComment({ ...repo, issue_number: number, body });
    },
    async closeIssue(number) {
      await octokit.rest.issues.update({ ...repo, issue_number: number, state: "closed", state_reason: "completed" });
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
