// Repository settings that live in the GitHub API, not in the codebase, as
// the workflows rely on them. Idempotent; safe to run again.
//
// Usage: GH_TOKEN=$(gh auth token) node .github/ci/repo-settings.ts
//
// Run by hand, by a repository admin; no workflow runs it, and no workflow
// token could: each call needs administration rights. It:
//
//   1. Leaves the repository's visibility untouched: that is a person's call.
//      The current value is printed, so the run says what it assumes.
//   2. Allows merge commits, which merge-green.ts merges with (every merge
//      into main is one), and deletes a pull request's head branch on merge:
//      main is the only long-lived branch.
//   3. Enables private vulnerability reporting, whose form SECURITY.md and
//      the issue forms send reporters to, and Dependabot alerts, which
//      Renovate's vulnerabilityAlerts reads (renovate.json).
//   4. Admits main alone to deploy to the automerge environment, whose
//      AUTOMERGE_TOKEN only main's copy of a workflow may read
//      (auto-merge.yaml). The secret itself is left alone.
//   5. Creates or updates a branch ruleset on main: no deletion, no force
//      push, and the "All checks passed" check required — not up to date with
//      main, which auto-merge.yaml relies on to merge each green pull request
//      at once.
//
// Rulesets rather than classic branch protection: they are additive, never
// clobbering protection set elsewhere. Another ruleset on main is listed,
// never changed: one this run replaces is a person's to remove.

import { Octokit } from "octokit";

import { info, warning } from "./lib/actions.ts";
import { optionalEnv, requireEnv } from "./lib/env.ts";
import { parseRepo, type Repo } from "./lib/github.ts";
import { runMain } from "./lib/main.ts";

export const DEFAULT_REPOSITORY = "oleg-tkachuk/limes";
export const RULESET_NAME = "protect-main";
export const MAIN_BRANCH = "main";
export const MAIN_REF = `refs/heads/${MAIN_BRANCH}`;

// REQUIRED_CONTEXTS are check-run names, which are the jobs' `name:` in
// ci.yaml, not their ids: the required check's (the checks job), which
// stands for every job a merge needs. repo-settings.test.ts holds it to
// ci.yaml; a context no job reports would block every merge for good.
export const REQUIRED_CONTEXTS: readonly string[] = ["All checks passed"];

// ENVIRONMENTS are the environments a workflow deploys to, each admitting
// main alone; repo-settings.test.ts holds them to the workflows.
export const ENVIRONMENTS: readonly string[] = ["automerge"];

// ADMIN_ROLE_ID is the built-in admin RepositoryRole: a repository outside an
// organisation has no per-user actor, so this is how its owner is named.
export const ADMIN_ROLE_ID = 5;
// The admin may merge a pull request the required check holds back — a check
// that cannot run never locks main against its maintainer — and may push
// nothing to main directly: every change goes through a pull request
// (CONTRIBUTING.md).
export const BYPASS_MODE = "pull_request";

export interface Ruleset {
  name: string;
  target: "branch";
  enforcement: "active";
  bypass_actors: { actor_id: number; actor_type: "RepositoryRole"; bypass_mode: "pull_request" }[];
  conditions: { ref_name: { include: string[]; exclude: string[] } };
  rules: (
    | { type: "deletion" }
    | { type: "non_fast_forward" }
    | { type: "required_status_checks"; parameters: { strict_required_status_checks_policy: boolean; required_status_checks: { context: string }[] } }
  )[];
}

// mainRuleset is the ruleset on main this run keeps.
export function mainRuleset(contexts: readonly string[] = REQUIRED_CONTEXTS): Ruleset {
  return {
    name: RULESET_NAME,
    target: "branch",
    enforcement: "active",
    bypass_actors: [{ actor_id: ADMIN_ROLE_ID, actor_type: "RepositoryRole", bypass_mode: BYPASS_MODE }],
    conditions: { ref_name: { include: [MAIN_REF], exclude: [] } },
    rules: [
      { type: "deletion" },
      { type: "non_fast_forward" },
      {
        type: "required_status_checks",
        parameters: { strict_required_status_checks_policy: false, required_status_checks: contexts.map((context) => ({ context })) },
      },
    ],
  };
}

// RulesetsApi is what the upsert asks of the API.
export interface RulesetsApi {
  list(): Promise<{ id: number; name: string }[]>;
  create(ruleset: Ruleset): Promise<void>;
  update(id: number, ruleset: Ruleset): Promise<void>;
}

// upsertRuleset updates the ruleset carrying its name, or creates it when
// none does, and reports what it did and which other rulesets there are.
export async function upsertRuleset(api: RulesetsApi, ruleset: Ruleset): Promise<string[]> {
  const existing = await api.list();
  const found = existing.find((r) => r.name === ruleset.name);
  const report = [];
  if (found === undefined) {
    await api.create(ruleset);
    report.push(`created ruleset ${ruleset.name}`);
  } else {
    await api.update(found.id, ruleset);
    report.push(`updated ruleset #${found.id} ${ruleset.name}`);
  }
  for (const other of existing.filter((r) => r.name !== ruleset.name)) {
    report.push(`left alone: ruleset #${other.id} ${other.name}`);
  }
  return report;
}

// BranchPoliciesApi is what admitting branches to an environment asks of
// the API.
export interface BranchPoliciesApi {
  restrict(environment: string): Promise<void>;
  list(environment: string): Promise<{ id: number; name: string; type?: string | undefined }[]>;
  create(environment: string, branch: string): Promise<void>;
  remove(environment: string, id: number): Promise<void>;
}

// admitOnly restricts environment to custom deployment branch policies and
// leaves branch alone among them: it adds it when missing, and removes every
// other, which would let that branch's copy of a workflow read the
// environment's secrets.
export async function admitOnly(api: BranchPoliciesApi, environment: string, branch: string): Promise<string[]> {
  await api.restrict(environment);
  const policies = await api.list(environment);
  const report = [];
  if (!policies.some((p) => p.name === branch && p.type !== "tag")) {
    await api.create(environment, branch);
    report.push(`${environment}: admitted ${branch}`);
  }
  for (const other of policies.filter((p) => p.name !== branch || p.type === "tag")) {
    await api.remove(environment, other.id);
    report.push(`${environment}: removed ${other.type ?? "branch"} policy ${other.name}`);
  }
  if (report.length === 0) {
    report.push(`${environment}: admits ${branch} alone already`);
  }
  return report;
}

// rulesetsOver is the RulesetsApi of repo through octokit.
function rulesetsOver(octokit: Octokit, repo: Repo): RulesetsApi {
  return {
    async list() {
      const { data } = await octokit.rest.repos.getRepoRulesets({ ...repo });
      return data.map((r) => ({ id: r.id, name: r.name }));
    },
    async create(ruleset) {
      await octokit.rest.repos.createRepoRuleset({ ...repo, ...ruleset });
    },
    async update(id, ruleset) {
      await octokit.rest.repos.updateRepoRuleset({ ...repo, ruleset_id: id, ...ruleset });
    },
  };
}

// branchPoliciesOver is the BranchPoliciesApi of repo through octokit.
function branchPoliciesOver(octokit: Octokit, repo: Repo): BranchPoliciesApi {
  return {
    async restrict(environment) {
      await octokit.rest.repos.createOrUpdateEnvironment({
        ...repo,
        environment_name: environment,
        deployment_branch_policy: { protected_branches: false, custom_branch_policies: true },
      });
    },
    async list(environment) {
      const { data } = await octokit.rest.repos.listDeploymentBranchPolicies({ ...repo, environment_name: environment });
      return data.branch_policies.map((p) => ({ id: p.id ?? 0, name: p.name ?? "", type: p.type }));
    },
    async create(environment, branch) {
      await octokit.rest.repos.createDeploymentBranchPolicy({ ...repo, environment_name: environment, name: branch, type: "branch" });
    },
    async remove(environment, id) {
      await octokit.rest.repos.deleteDeploymentBranchPolicy({ ...repo, environment_name: environment, branch_policy_id: id });
    },
  };
}

// tryEnable runs one enabling call; one already on, or not offered to this
// repository, is a warning, not a failure.
async function tryEnable(what: string, call: () => Promise<unknown>): Promise<void> {
  try {
    await call();
    info(`    ${what}: enabled`);
  } catch (err) {
    warning(`${what}: could not enable (already on, or not available here): ${err instanceof Error ? err.message : String(err)}`);
  }
}

async function main(): Promise<void> {
  const repo = parseRepo(optionalEnv("REPO") ?? DEFAULT_REPOSITORY);
  const octokit = new Octokit({ auth: requireEnv("GH_TOKEN") });

  const { data } = await octokit.rest.repos.get({ ...repo });
  info(`Repository: ${repo.owner}/${repo.repo}`);
  info(`Visibility (left unchanged): ${data.visibility ?? "unknown"}`);

  info("==> Merge commits, and head branches deleted on merge");
  const { data: updated } = await octokit.rest.repos.update({ ...repo, allow_merge_commit: true, delete_branch_on_merge: true });
  info(`    allow_merge_commit = ${String(updated.allow_merge_commit)}, delete_branch_on_merge = ${String(updated.delete_branch_on_merge)}`);

  info("==> Private vulnerability reporting and Dependabot alerts");
  await tryEnable("private vulnerability reporting", () => octokit.rest.repos.enablePrivateVulnerabilityReporting({ ...repo }));
  await tryEnable("Dependabot alerts", () => octokit.rest.repos.enableVulnerabilityAlerts({ ...repo }));

  info(`==> Environments admitting ${MAIN_BRANCH} alone: ${ENVIRONMENTS.join(", ")}`);
  const policies = branchPoliciesOver(octokit, repo);
  for (const environment of ENVIRONMENTS) {
    for (const line of await admitOnly(policies, environment, MAIN_BRANCH)) info(`    ${line}`);
  }

  info(`==> Branch ruleset for ${MAIN_BRANCH}, requiring ${REQUIRED_CONTEXTS.join(", ")}`);
  for (const line of await upsertRuleset(rulesetsOver(octokit, repo), mainRuleset())) info(`    ${line}`);
  info("Done.");
}

if (import.meta.main) {
  await runMain(main);
}
