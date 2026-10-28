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

// RunState is the newest finished run of a workflow: its id, how it ended,
// the commit it ran for, and where to read it.
export interface RunState {
  id: number;
  conclusion: string;
  headSha: string;
  url: string;
}

// PullRequestRun is a pull_request run of a workflow: whether and how it
// ended, the title it ran under — the pull request's title then, which its
// checks read — and the pull requests, with their base, it ran for.
export interface PullRequestRun {
  status: string;
  conclusion: string | undefined;
  title: string;
  pullRequests: { number: number; baseRef: string }[];
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
  // fileAt is the text of the file at path in ref, or undefined when ref
  // holds none.
  fileAt(path: string, ref: string): Promise<string | undefined>;
  // merge merges a pull request with a merge commit whose body is message,
  // refused unless its head is sha.
  merge(number: number, sha: string, message: string): Promise<void>;
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
  // finishedRunsOf is the ids of the completed push runs of the workflow
  // file on branch that tested sha, whatever they concluded, newest first.
  finishedRunsOf(workflow: string, branch: string, sha: string): Promise<number[]>;
  // latestRunOf is the newest finished push run of the workflow file on
  // branch, if any.
  latestRunOf(workflow: string, branch: string): Promise<RunState | undefined>;
  // openPullRequestsInto is every open, ready pull request from this
  // repository into base.
  openPullRequestsInto(base: string): Promise<OpenPullRequest[]>;
  // pullRequestRuns is the pull_request runs of the workflow file at
  // headSha, newest first.
  pullRequestRuns(workflow: string, headSha: string): Promise<PullRequestRun[]>;
  // openIssueTitled is the number of the open issue titled title, if any.
  openIssueTitled(title: string): Promise<number | undefined>;
  createIssue(title: string, body: string): Promise<number>;
  commentOnIssue(number: number, body: string): Promise<void>;
  closeIssue(number: number): Promise<void>;
  // dispatch sends a repository_dispatch event of eventType with payload.
  dispatch(eventType: string, payload: Record<string, string>): Promise<void>;
}

// MERGE_METHOD is how the automation merges: a merge commit, whose body —
// the title the merged run checked — the release reads.
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
  return new OctokitGitHub(octokit, repo);
}

// workflowFile is a workflow's file name, which the REST API takes as its
// id, from its path.
function workflowFile(workflow: string): string {
  return workflow.split("/").at(-1) ?? workflow;
}

// OctokitGitHub is the GitHub of repo through octokit's REST API.
class OctokitGitHub implements GitHub {
  private readonly octokit: Octokit;
  private readonly repo: Repo;

  constructor(octokit: Octokit, repo: Repo) {
    this.octokit = octokit;
    this.repo = repo;
  }

  async pullRequest(number: number): Promise<PullRequest> {
    const { data } = await this.octokit.rest.pulls.get({ ...this.repo, pull_number: number });
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
  }

  async permission(login: string): Promise<string | undefined> {
    try {
      const { data } = await this.octokit.rest.repos.getCollaboratorPermissionLevel({ ...this.repo, username: login });
      return data.permission;
    } catch (err) {
      if (statusOf(err) === NOT_FOUND) {
        return undefined;
      }
      throw err;
    }
  }

  async compare(base: string, head: string): Promise<Comparison> {
    const { data } = await this.octokit.rest.repos.compareCommitsWithBasehead({ ...this.repo, basehead: `${base}...${head}` });
    return {
      behindBy: data.behind_by,
      files: (data.files ?? []).map((f) => ({ filename: f.filename, previousFilename: f.previous_filename })),
    };
  }

  async fileAt(path: string, ref: string): Promise<string | undefined> {
    try {
      const { data } = await this.octokit.rest.repos.getContent({ ...this.repo, path, ref, mediaType: { format: "raw" } });
      return String(data);
    } catch (err) {
      if (statusOf(err) === NOT_FOUND) {
        return undefined;
      }
      throw err;
    }
  }

  async merge(number: number, sha: string, message: string): Promise<void> {
    await this.octokit.rest.pulls.merge({ ...this.repo, pull_number: number, sha, merge_method: MERGE_METHOD, commit_message: message });
  }

  async workflowRun(runId: number): Promise<WorkflowRun> {
    const { data } = await this.octokit.rest.actions.getWorkflowRun({ ...this.repo, run_id: runId });
    return {
      path: data.path,
      event: data.event,
      headBranch: data.head_branch ?? "",
      headSha: data.head_sha,
      repository: data.repository.full_name,
    };
  }

  async runJobs(runId: number): Promise<Job[]> {
    const jobs = await this.octokit.paginate(this.octokit.rest.actions.listJobsForWorkflowRun, { ...this.repo, run_id: runId, per_page: PAGE_SIZE });
    return jobs.map((j) => ({ name: j.name, status: j.status, conclusion: j.conclusion ?? undefined }));
  }

  async releaseExists(tag: string): Promise<boolean> {
    return existsBy(() => this.octokit.rest.repos.getReleaseByTag({ ...this.repo, tag }));
  }

  async publishRelease(release: NewRelease): Promise<void> {
    // The tag must be pushed already: a release of a missing tag would
    // create it at the default branch's head.
    if (!(await existsBy(() => this.octokit.rest.git.getRef({ ...this.repo, ref: `tags/${release.tag}` })))) {
      throw new Error(`tag ${release.tag} is not in the repository`);
    }
    const { data: generated } = await this.octokit.rest.repos.generateReleaseNotes({
      ...this.repo,
      tag_name: release.tag,
      ...previousTagOf(release),
    });
    const body = release.notes === "" ? generated.body : `${release.notes}\n${generated.body}`;
    try {
      await this.octokit.rest.repos.createRelease({
        ...this.repo,
        tag_name: release.tag,
        name: release.tag,
        body,
        ...standingOf(release),
      });
    } catch (err) {
      if (!(await this.releaseExists(release.tag))) {
        throw err;
      }
    }
  }

  async sameRepoPullRequest(head: string, base: string): Promise<number | undefined> {
    const { data } = await this.octokit.rest.pulls.list({ ...this.repo, head: `${this.repo.owner}:${head}`, base, state: "open" });
    return data.find((p) => p.head.repo?.full_name === `${this.repo.owner}/${this.repo.repo}`)?.number;
  }

  async createPullRequest(head: string, base: string, text: PullRequestText): Promise<number> {
    const { data } = await this.octokit.rest.pulls.create({ ...this.repo, head, base, title: text.title, body: text.body });
    return data.number;
  }

  async editPullRequest(number: number, text: PullRequestText): Promise<void> {
    await this.octokit.rest.pulls.update({ ...this.repo, pull_number: number, title: text.title, body: text.body });
  }

  async finishedRunsOf(workflow: string, branch: string, sha: string): Promise<number[]> {
    const { data } = await this.octokit.rest.actions.listWorkflowRuns({
      ...this.repo,
      workflow_id: workflowFile(workflow),
      branch,
      event: "push",
      status: "completed",
      head_sha: sha,
      per_page: PAGE_SIZE,
    });
    return data.workflow_runs.sort((a, b) => b.run_number - a.run_number).map((r) => r.id);
  }

  async latestRunOf(workflow: string, branch: string): Promise<RunState | undefined> {
    const { data } = await this.octokit.rest.actions.listWorkflowRuns({
      ...this.repo,
      workflow_id: workflowFile(workflow),
      branch,
      event: "push",
      status: "completed",
      per_page: 1,
    });
    const run = data.workflow_runs[0];
    return run === undefined ? undefined : { id: run.id, conclusion: run.conclusion ?? "", headSha: run.head_sha, url: run.html_url };
  }

  async openPullRequestsInto(base: string): Promise<OpenPullRequest[]> {
    const pulls = await this.octokit.paginate(this.octokit.rest.pulls.list, { ...this.repo, base, state: "open", per_page: PAGE_SIZE });
    return pulls
      .filter((p) => !(p.draft ?? false) && p.head.repo?.full_name === `${this.repo.owner}/${this.repo.repo}`)
      .map((p) => ({ number: p.number, headSha: p.head.sha }));
  }

  async pullRequestRuns(workflow: string, headSha: string): Promise<PullRequestRun[]> {
    const { data } = await this.octokit.rest.actions.listWorkflowRuns({
      ...this.repo,
      workflow_id: workflowFile(workflow),
      event: "pull_request",
      head_sha: headSha,
      per_page: PAGE_SIZE,
    });
    return data.workflow_runs
      .sort((a, b) => b.run_number - a.run_number)
      .map((r) => ({
        status: r.status ?? "",
        conclusion: r.conclusion ?? undefined,
        title: r.display_title,
        pullRequests: (r.pull_requests ?? []).map((p) => ({ number: p.number, baseRef: p.base.ref })),
      }));
  }

  async openIssueTitled(title: string): Promise<number | undefined> {
    const issues = await this.octokit.paginate(this.octokit.rest.issues.listForRepo, { ...this.repo, state: "open", per_page: PAGE_SIZE });
    return issues.find((i) => i.pull_request === undefined && i.title === title)?.number;
  }

  async createIssue(title: string, body: string): Promise<number> {
    const { data } = await this.octokit.rest.issues.create({ ...this.repo, title, body });
    return data.number;
  }

  async commentOnIssue(number: number, body: string): Promise<void> {
    await this.octokit.rest.issues.createComment({ ...this.repo, issue_number: number, body });
  }

  async closeIssue(number: number): Promise<void> {
    await this.octokit.rest.issues.update({ ...this.repo, issue_number: number, state: "closed", state_reason: "completed" });
  }

  async dispatch(eventType: string, payload: Record<string, string>): Promise<void> {
    await this.octokit.rest.repos.createDispatchEvent({ ...this.repo, event_type: eventType, client_payload: payload });
  }
}

// previousTagOf is the release notes' starting point, when the release
// names one; GitHub picks its own otherwise.
function previousTagOf(release: NewRelease): { previous_tag_name?: string } {
  return release.previousTag === undefined ? {} : { previous_tag_name: release.previousTag };
}

// standingOf is how a release stands among the others: a prerelease, or
// one not the latest, says so; GitHub makes any other the latest.
function standingOf(release: NewRelease): { make_latest?: typeof MAKE_LATEST_FALSE; prerelease?: true } {
  const prerelease = release.prerelease === true;
  return {
    ...(release.latest && !prerelease ? {} : { make_latest: MAKE_LATEST_FALSE }),
    ...(prerelease ? { prerelease: true as const } : {}),
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
