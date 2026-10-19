import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

import { parse } from "yaml";

import { fakeData, fakeGitHub } from "./lib/fake-github.ts";
import { RELEASE_EVENT, dispatchRelease } from "./dispatch-release.ts";

test("dispatches the commit and the run that vouches for it", async () => {
  const data = fakeData();
  await dispatchRelease(fakeGitHub(data), "abc", "42");
  assert.deepEqual(data.dispatched, [{ eventType: RELEASE_EVENT, payload: { sha: "abc", ci_run_id: "42" } }]);
});

test("release.yaml runs on that event", () => {
  const workflow = parse(readFileSync(new URL("../workflows/release.yaml", import.meta.url), "utf8")) as { on: { repository_dispatch: { types: string[] } } };
  assert.deepEqual(workflow.on.repository_dispatch.types, [RELEASE_EVENT]);
});
