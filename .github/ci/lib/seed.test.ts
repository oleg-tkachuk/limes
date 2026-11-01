import assert from "node:assert/strict";
import { test } from "node:test";

import { SEED_VERSION, seedLine } from "./seed.ts";
import { tempRepo } from "./temp-repo.ts";
import type { Module } from "./modules.ts";

// MODULE is the nested module the seed tests start a line of.
const MODULE: Module = { name: "attest", dir: "attest", prefix: "attest/", table: undefined };
const TAG = `${MODULE.prefix}${SEED_VERSION}`;
// MERGER signs the merge commit, as the test repository's commits are.
const MERGER = ["-c", "user.name=test", "-c", "user.email=test@example.invalid"];

// A module a pull request brought is seeded on the merge that brought it to
// main, not on the branch's first commit, whose tree nobody reviewed.
test("seedLine tags the first-parent commit that brought the module", async () => {
  const repo = await tempRepo();
  try {
    repo.write("README.md", "main");
    await repo.commit("main");
    await repo.run("switch", "--quiet", "-c", "contribution");
    repo.write("attest/go.mod", "module example.com/attest\n");
    repo.write("attest/a.go", "package attest // first, never reviewed\n");
    const first = await repo.commit("add attest");
    repo.write("attest/a.go", "package attest // reviewed\n");
    await repo.commit("rework attest");
    await repo.run("switch", "--quiet", "main");
    await repo.run(...MERGER, "merge", "--quiet", "--no-ff", "-m", "merge attest", "contribution");
    const merge = (await repo.run("rev-parse", "HEAD")).trim();

    const pushed: string[] = [];
    const tag = await seedLine(repo.dir, MODULE, async (t) => {
      pushed.push(t);
    });
    assert.equal(tag, TAG);
    assert.deepEqual(pushed, [TAG]);
    const tagged = (await repo.run("rev-list", "-n", "1", TAG)).trim();
    assert.equal(tagged, merge, `seeded on ${tagged}, the branch's first commit is ${first}`);
  } finally {
    repo.cleanup();
  }
});

// A module added on main itself is seeded on that commit.
test("seedLine tags a module added on main where it was added", async () => {
  const repo = await tempRepo();
  try {
    repo.write("attest/go.mod", "module example.com/attest\n");
    const added = await repo.commit("add attest");
    repo.write("README.md", "later");
    await repo.commit("later");
    await seedLine(repo.dir, MODULE, async () => {});
    assert.equal((await repo.run("rev-list", "-n", "1", TAG)).trim(), added);
  } finally {
    repo.cleanup();
  }
});
