import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import { describe, test } from "node:test";

import { parse } from "yaml";

import {
  ADMIN_ROLE_ID,
  ENVIRONMENTS,
  MAIN_REF,
  REQUIRED_CONTEXTS,
  RULESET_NAME,
  admitOnly,
  mainRuleset,
  upsertRuleset,
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

test("main is kept from deletion and force pushes, and requires the check, not up to date", () => {
  const r = mainRuleset();
  assert.deepEqual(r.conditions.ref_name.include, [MAIN_REF]);
  assert.deepEqual(r.rules, [
    { type: "deletion" },
    { type: "non_fast_forward" },
    {
      type: "required_status_checks",
      parameters: { strict_required_status_checks_policy: false, required_status_checks: REQUIRED_CONTEXTS.map((context) => ({ context })) },
    },
  ]);
});

// The admin may merge a pull request past a check that cannot run, and push
// nothing to main past the ruleset.
test("the admin bypasses the ruleset for a pull request only", () => {
  assert.deepEqual(mainRuleset().bypass_actors, [{ actor_id: ADMIN_ROLE_ID, actor_type: "RepositoryRole", bypass_mode: "pull_request" }]);
});

describe("upsertRuleset", () => {
  function fake(existing: { id: number; name: string }[]) {
    const calls: string[] = [];
    const api: RulesetsApi = {
      list: async () => existing,
      create: async (r: Ruleset) => void calls.push(`create ${r.name}`),
      update: async (id: number, r: Ruleset) => void calls.push(`update ${id} ${r.name}`),
    };
    return { api, calls };
  }
  test("the ruleset of its name is updated, and another left alone", async () => {
    const f = fake([{ id: 3, name: "older" }, { id: 7, name: RULESET_NAME }]);
    const report = await upsertRuleset(f.api, mainRuleset());
    assert.deepEqual(f.calls, [`update 7 ${RULESET_NAME}`]);
    assert.ok(report.includes("left alone: ruleset #3 older"));
  });
  test("none of its name is created", async () => {
    const f = fake([{ id: 3, name: "older" }]);
    await upsertRuleset(f.api, mainRuleset());
    assert.deepEqual(f.calls, [`create ${RULESET_NAME}`]);
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
