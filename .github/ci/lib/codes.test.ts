import assert from "node:assert/strict";
import { describe, test } from "node:test";

import { CODES_HEADING, ERRORS_DOC, breaksWire, codeChanges, compare, contractIn, describeChange, withCodeChanges, type Answer } from "./codes.ts";
import { tempRepo } from "./temp-repo.ts";

// Row is a code table row: its code, status and category.
type Row = [code: string, status: string, category: string];

// doc is an errors.md with limes's, the meter's and the exchange's tables.
function doc(limes: Row[], meter: Row[], exchange: Row[]): string {
  const rows = (table: Row[]) => table.map(([c, s, k]) => `| \`${c}\` | ${s} | \`Err\` |  |  | Title | \`${k}\` |`).join("\n");
  return `# Errors\n\n| \`table_of_members\` | 200 | not a code | \`x\` |\n\n## limes's codes\n\n${rows(limes)}\n\n## The meter's codes\n\n${rows(meter)}\n\n## The exchange's codes\n\n${rows(exchange)}\n`;
}

const answer = (status: string, category: string): Answer => ({ status, category });

describe("contractIn", () => {
  test("reads each code of limes's, the meter's and the exchange's tables with its answer, and nothing above", () => {
    const got = contractIn(doc([["revoked", "401", "unauthenticated"]], [["lease_nested", "409", "precondition"]], [["invalid_grant", "400", "unauthenticated"]]));
    assert.deepEqual(
      [...got].sort(),
      [
        ["invalid_grant", answer("400", "unauthenticated")],
        ["lease_nested", answer("409", "precondition")],
        ["revoked", answer("401", "unauthenticated")],
      ],
    );
  });
  test("reads no row of a table without a category column: a wire code that is no limes code", () => {
    const wire = "\n\n| Code | HTTP | Sentinel | Description |\n|---|---|---|---|\n| `use_dpop_nonce` | 400 | `dpop.ErrNonceRequired` | A fresh nonce goes in `DPoP-Nonce`. |\n";
    const got = contractIn(doc([["revoked", "401", "unauthenticated"]], [], []) + wire);
    assert.deepEqual([...got.keys()], ["revoked"]);
  });
  test("reads nothing from a document with no code tables", () => {
    assert.equal(contractIn("# Errors\n").size, 0);
  });
});

describe("compare", () => {
  const was = new Map([
    ["kept", answer("400", "invalid")],
    ["moved", answer("409", "conflict")],
    ["restatused", answer("409", "invalid")],
    ["gone", answer("404", "not_found")],
  ]);
  const is = new Map([
    ["kept", answer("400", "invalid")],
    ["moved", answer("409", "already_exists")],
    ["restatused", answer("400", "invalid")],
    ["new", answer("500", "internal")],
  ]);
  test("names codes added, removed and answered otherwise", () => {
    const changes = compare(was, is);
    assert.deepEqual(changes.added, ["new"]);
    assert.deepEqual(changes.removed, ["gone"]);
    assert.deepEqual(
      changes.changed.map(describeChange),
      ["`moved` — category conflict → already_exists", "`restatused` — status 409 → 400"],
    );
    assert.equal(breaksWire(changes), true);
  });
  test("a code added breaks no consumer; one removed or changed does", () => {
    assert.equal(breaksWire(compare(was, new Map([...was, ["new", answer("500", "internal")]]))), false);
    assert.equal(breaksWire(compare(was, was)), false);
  });
});

describe("withCodeChanges", () => {
  const entry = "## [1.2.0](u) (2026-11-03)\n\n### Features\n\n* a\n";
  test("adds a section naming the codes added, removed and changed, last", () => {
    assert.equal(
      withCodeChanges(entry, { added: ["a_code"], removed: ["old_code"], changed: [{ code: "moved", from: answer("409", "conflict"), to: answer("409", "already_exists") }] }),
      `${entry.trimEnd()}\n\n${CODES_HEADING}\n\n* added: \`a_code\`\n* removed: \`old_code\`\n* changed: \`moved\` — category conflict → already_exists\n`,
    );
  });
  test("leaves an entry with no code change as it is", () => {
    assert.equal(withCodeChanges(entry, { added: [], removed: [], changed: [] }), entry);
  });
});

test("codeChanges compares the tables at two revisions, and finds none before the document existed", async () => {
  const repo = await tempRepo();
  try {
    repo.write("README.md", "start");
    await repo.commit("start");
    await repo.run("tag", "v0.1.0");
    repo.write(ERRORS_DOC, doc([["revoked", "401", "unauthenticated"], ["gone_code", "404", "not_found"]], [["lease_nested", "409", "conflict"]], []));
    await repo.commit("docs");
    await repo.run("tag", "v0.2.0");
    repo.write(ERRORS_DOC, doc([["revoked", "401", "unauthenticated"]], [["lease_nested", "409", "precondition"], ["feature_disabled", "500", "precondition"]], [["new_exchange_code", "400", "invalid"]]));
    await repo.commit("codes");
    const changes = await codeChanges(repo.dir, "v0.2.0", "HEAD");
    assert.deepEqual([changes.added, changes.removed, changes.changed.map(describeChange)], [["feature_disabled", "new_exchange_code"], ["gone_code"], ["`lease_nested` — category conflict → precondition"]]);
    assert.equal(breaksWire(await codeChanges(repo.dir, "v0.1.0", "HEAD")), false);
  } finally {
    repo.cleanup();
  }
});
