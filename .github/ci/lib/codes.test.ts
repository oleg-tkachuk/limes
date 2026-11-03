import assert from "node:assert/strict";
import { describe, test } from "node:test";

import { CODES_HEADING, ERRORS_DOC, codeChanges, codesIn, withCodeChanges } from "./codes.ts";
import { tempRepo } from "./temp-repo.ts";

// doc is an errors.md with limes's, the meter's and the exchange's tables.
function doc(limes: string[], meter: string[], exchange: string[]): string {
  const rows = (codes: string[]) => codes.map((c) => `| \`${c}\` | 400 | \`Err\` |  |  | Title | \`invalid\` |`).join("\n");
  return `# Errors\n\n| \`table_of_members\` | 200 | not a code table |\n\n## limes's codes\n\n${rows(limes)}\n\n## The meter's codes\n\n${rows(meter)}\n\n## The exchange's codes\n\n${rows(exchange)}\n`;
}

describe("codesIn", () => {
  test("reads limes's and the meter's tables, and neither the exchange's nor anything above", () => {
    assert.deepEqual([...codesIn(doc(["revoked"], ["lease_nested"], ["invalid_grant"]))].sort(), ["lease_nested", "revoked"]);
  });
  test("reads nothing from a document with no code tables", () => {
    assert.equal(codesIn("# Errors\n").size, 0);
  });
});

describe("withCodeChanges", () => {
  const entry = "## [1.2.0](u) (2026-11-03)\n\n### Features\n\n* a\n";
  test("adds a section naming the codes added and removed, last", () => {
    assert.equal(
      withCodeChanges(entry, { added: ["a_code", "b_code"], removed: ["old_code"] }),
      `${entry.trimEnd()}\n\n${CODES_HEADING}\n\n* added: \`a_code\`, \`b_code\`\n* removed: \`old_code\`\n`,
    );
  });
  test("names only what changed", () => {
    assert.equal(withCodeChanges(entry, { added: [], removed: ["old_code"] }), `${entry.trimEnd()}\n\n${CODES_HEADING}\n\n* removed: \`old_code\`\n`);
  });
  test("leaves an entry with no code change as it is", () => {
    assert.equal(withCodeChanges(entry, { added: [], removed: [] }), entry);
  });
});

test("codeChanges compares the tables at two revisions, and finds none before the document existed", async () => {
  const repo = await tempRepo();
  try {
    repo.write("README.md", "start");
    await repo.commit("start");
    await repo.run("tag", "v0.1.0");
    repo.write(ERRORS_DOC, doc(["revoked", "gone_code"], ["lease_nested"], []));
    await repo.commit("docs");
    await repo.run("tag", "v0.2.0");
    repo.write(ERRORS_DOC, doc(["revoked"], ["lease_nested", "feature_disabled"], ["new_exchange_code"]));
    await repo.commit("codes");
    assert.deepEqual(await codeChanges(repo.dir, "v0.2.0", "HEAD"), { added: ["feature_disabled"], removed: ["gone_code"] });
    assert.deepEqual(await codeChanges(repo.dir, "v0.1.0", "HEAD"), { added: [], removed: [] });
  } finally {
    repo.cleanup();
  }
});
