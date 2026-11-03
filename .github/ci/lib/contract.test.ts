import assert from "node:assert/strict";
import { test } from "node:test";

import { CONTRACT_DOCS, CONTRACT_HEADING, contractChanges, withContractChanges } from "./contract.ts";
import { tempRepo } from "./temp-repo.ts";

test("contractChanges names each commit changing the policy since a revision, oldest first, without its type", async () => {
  const repo = await tempRepo();
  try {
    const [policy = ""] = CONTRACT_DOCS;
    repo.write(policy, "start");
    await repo.commit("docs: start");
    await repo.run("tag", "v0.1.0");
    repo.write(policy, "defaults are contract");
    await repo.commit("docs(compat): make defaults contract");
    repo.write("README.md", "elsewhere");
    await repo.commit("docs: touch the readme");
    repo.write(policy, "v1 together");
    await repo.commit("ci!: nested modules reach v1 with limes");
    assert.deepEqual(await contractChanges(repo.dir, "v0.1.0", "HEAD"), ["make defaults contract", "nested modules reach v1 with limes"]);
    assert.deepEqual(await contractChanges(repo.dir, "HEAD", "HEAD"), []);
  } finally {
    repo.cleanup();
  }
});

test("withContractChanges adds a section naming each change, last, and leaves an entry with none as it is", () => {
  const entry = "## [0.157.0](u) (2026-11-03)\n\n### Features\n\n* a\n";
  assert.equal(withContractChanges(entry, ["a", "b"]), `${entry.trimEnd()}\n\n${CONTRACT_HEADING}\n\n* a\n* b\n`);
  assert.equal(withContractChanges(entry, []), entry);
});
