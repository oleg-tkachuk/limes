import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { describe, test } from "node:test";

import { parse } from "yaml";

import {
  ADMIN_ROLE_ID,
  ENVIRONMENTS,
  GITHUB_ACTIONS_APP_ID,
  MAIN_REF,
  RELEASE_TAG_REFS,
  REQUIRED_CONTEXTS,
  RULESET_NAME,
  TAG_CREATION_RULESET_NAME,
  TAGS_RULESET_NAME,
  admitOnly,
  mainRuleset,
  tagCreationRuleset,
  tagsRuleset,
  upsertRulesets,
  type BranchPoliciesApi,
  type Ruleset,
  type RulesetsApi,
} from "./repo-settings.ts";

const WORKFLOWS = new URL("../workflows/", import.meta.url);

type Workflow = { jobs: Record<string, { name?: string; environment?: string | { name: string } }> };
const workflows = (): Workflow[] =>
  readdirSync(WORKFLOWS)
    .filter((f) => f.endsWith(".yaml"))
    .map((f) => parse(readFileSync(new URL(f, WORKFLOWS), "utf8")) as Workflow);

// A required context no job reports blocks every merge, silently: it is the
// name of ci.yaml's required check, which merge-green.ts and the release
// read as standing for every job a merge needs.
test("the required context is ci.yaml's required check", () => {
  const ci = parse(readFileSync(new URL("ci.yaml", WORKFLOWS), "utf8")) as Workflow;
  assert.deepEqual(REQUIRED_CONTEXTS, [ci.jobs.checks?.name]);
});

// An environment a workflow deploys to that the run leaves unrestricted
// would let any branch's copy of that workflow read its secrets.
test("every environment a workflow deploys to admits main alone", () => {
  const used = new Set<string>();
  for (const w of workflows()) {
    for (const job of Object.values(w.jobs)) {
      const env = job.environment;
      if (env !== undefined) used.add(typeof env === "string" ? env : env.name);
    }
  }
  assert.deepEqual([...used].sort(), [...ENVIRONMENTS].sort());
});

test("main is kept from deletion and force pushes, and takes a merged pull request whose check GitHub Actions passed", () => {
  const r = mainRuleset();
  assert.equal(r.target, "branch");
  assert.deepEqual(r.conditions.ref_name.include, [MAIN_REF]);
  assert.deepEqual(r.rules, [
    { type: "deletion" },
    { type: "non_fast_forward" },
    {
      type: "pull_request",
      parameters: {
        required_approving_review_count: 0,
        dismiss_stale_reviews_on_push: false,
        require_code_owner_review: false,
        require_last_push_approval: false,
        required_review_thread_resolution: false,
        allowed_merge_methods: ["merge"],
      },
    },
    {
      type: "required_status_checks",
      parameters: {
        strict_required_status_checks_policy: false,
        required_status_checks: REQUIRED_CONTEXTS.map((context) => ({ context, integration_id: GITHUB_ACTIONS_APP_ID })),
      },
    },
  ]);
});

// The merge method main takes is the one merge-green.ts merges with.
test("main takes the merge method auto-merge merges with", () => {
  const github = readFileSync(new URL("lib/github.ts", import.meta.url), "utf8");
  assert.match(github, /const MERGE_METHOD = "merge"/);
});

// globToRegExp reads a ruleset ref pattern as GitHub does: a * stands for
// anything but a slash.
const globToRegExp = (glob: string) => new RegExp(`^${glob.replace(/[.+?^${}()|[\]\\]/g, "\\$&").replaceAll("*", "[^/]*")}$`);

test("every module's release tags are kept as they were cut", () => {
  const r = tagsRuleset();
  assert.equal(r.name, TAGS_RULESET_NAME);
  assert.equal(r.target, "tag");
  assert.deepEqual(r.bypass_actors, []);
  assert.deepEqual(r.rules, [{ type: "deletion" }, { type: "non_fast_forward" }, { type: "update" }]);
  const { MODULES, tagPrefix } = createRequire(import.meta.url)("../release/modules.cjs") as {
    MODULES: Record<string, { dir: string }>;
    tagPrefix: (dir: string) => string;
  };
  const patterns = RELEASE_TAG_REFS.map(globToRegExp);
  for (const [name, { dir }] of Object.entries(MODULES)) {
    const tag = `refs/tags/${tagPrefix(dir)}v1.2.3`;
    assert.ok(patterns.some((p) => p.test(tag)), `${name}: ${tag} is not kept`);
  }
  assert.ok(!patterns.some((p) => p.test("refs/tags/nightly")), "a tag no release cuts is kept too");
});

// Only the release workflow, through its deploy key, creates a release tag
// (GitHub admits the Actions app as a bypass actor in an organisation's
// repository alone); the rules that keep tags as cut stay in a ruleset nobody
// bypasses.
test("only a deploy key creates a release tag", () => {
  const r = tagCreationRuleset();
  assert.equal(r.name, TAG_CREATION_RULESET_NAME);
  assert.equal(r.target, "tag");
  assert.deepEqual(r.rules, [{ type: "creation" }]);
  assert.deepEqual(r.bypass_actors, [{ actor_id: null, actor_type: "DeployKey", bypass_mode: "always" }]);
  assert.deepEqual(r.conditions.ref_name.include, [...RELEASE_TAG_REFS]);
  assert.ok(!tagsRuleset().rules.some((rule) => rule.type === "creation"), "creation sits beside rules a bypass would pass too");
});

// The admin may merge a pull request past a check that cannot run, and push
// nothing to main past the ruleset.
test("the admin bypasses the ruleset for a pull request only", () => {
  assert.deepEqual(mainRuleset().bypass_actors, [{ actor_id: ADMIN_ROLE_ID, actor_type: "RepositoryRole", bypass_mode: "pull_request" }]);
});

describe("upsertRulesets", () => {
  function fake(existing: { id: number; name: string }[]) {
    const calls: string[] = [];
    const api: RulesetsApi = {
      list: async () => existing,
      create: async (r: Ruleset) => void calls.push(`create ${r.name}`),
      update: async (id: number, r: Ruleset) => void calls.push(`update ${id} ${r.name}`),
    };
    return { api, calls };
  }
  test("a ruleset of its name is updated, the rest created, and another left alone", async () => {
    const f = fake([{ id: 3, name: "older" }, { id: 7, name: RULESET_NAME }]);
    const report = await upsertRulesets(f.api, [mainRuleset(), tagsRuleset(), tagCreationRuleset()]);
    assert.deepEqual(f.calls, [`update 7 ${RULESET_NAME}`, `create ${TAGS_RULESET_NAME}`, `create ${TAG_CREATION_RULESET_NAME}`]);
    assert.deepEqual(report.filter((l) => l.startsWith("left alone")), ["left alone: ruleset #3 older"]);
  });
});

describe("admitOnly", () => {
  function fake(policies: { id: number; name: string; type?: string | undefined }[]) {
    const calls: string[] = [];
    const api: BranchPoliciesApi = {
      restrict: async (env) => void calls.push(`restrict ${env}`),
      list: async () => policies,
      create: async (env, branch) => void calls.push(`create ${env} ${branch}`),
      remove: async (env, id) => void calls.push(`remove ${env} ${id}`),
    };
    return { api, calls };
  }
  test("main is admitted to an environment that admits none", async () => {
    const f = fake([]);
    await admitOnly(f.api, "automerge", "main");
    assert.deepEqual(f.calls, ["restrict automerge", "create automerge main"]);
  });
  test("every other branch, and a tag of main's name, is removed", async () => {
    const f = fake([{ id: 1, name: "main", type: "branch" }, { id: 2, name: "release/*", type: "branch" }, { id: 3, name: "main", type: "tag" }]);
    await admitOnly(f.api, "automerge", "main");
    assert.deepEqual(f.calls, ["restrict automerge", "remove automerge 2", "remove automerge 3"]);
  });
  test("an environment admitting main alone is left as it is", async () => {
    const f = fake([{ id: 1, name: "main", type: "branch" }]);
    const report = await admitOnly(f.api, "automerge", "main");
    assert.deepEqual(f.calls, ["restrict automerge"]);
    assert.deepEqual(report, ["automerge: admits main alone already"]);
  });
});
