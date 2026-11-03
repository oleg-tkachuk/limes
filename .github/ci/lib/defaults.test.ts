import assert from "node:assert/strict";
import { describe, test } from "node:test";

import {
  DEFAULTS_DOC,
  DEFAULTS_HEADING,
  breaksDefaults,
  compareDefaults,
  defaultChanges,
  defaultsIn,
  describeDefaultChange,
  withDefaultChanges,
} from "./defaults.ts";
import { tempRepo } from "./temp-repo.ts";

// Row is a defaults table row: package.Config, field, the Default cell, and
// prose.
type Row = [config: string, field: string, value: string, prose?: string];

// doc is a defaults.md with a section of rows per module, and of constants,
// as package.Default… and value, when given.
function doc(sections: Record<string, Row[]>, constants: Record<string, [string, string][]> = {}): string {
  const table = (rows: Row[]) => rows.map(([c, f, v, p]) => `| \`${c}\` | \`${f}\` | ${v} | ${p ?? ""} |`).join("\n");
  const values = (rows: [string, string][] = []) => rows.map(([c, v]) => `| \`${c}\` | \`${v}\` |`).join("\n");
  const body = Object.entries(sections).map(
    ([module, rows]) =>
      `## ${module}\n\n| Config | Field | Default | Zero value |\n|---|---|---|---|\n${table(rows)}\n\n| Constant | Value |\n|---|---|\n${values(constants[module])}\n`,
  );
  return `# Defaults\n\n| \`x.NotAConfig\` | \`Above\` | any | section |\n\n${body.join("\n")}`;
}

describe("defaultsIn", () => {
  const d = doc(
    {
      limes: [["limes.VerifierConfig", "Leeway", "`DefaultLeeway`"]],
      pgstore: [["pgstore.Config", "DisableLeases", "off", "Leases open."]],
    },
    { limes: [["limes.DefaultLeeway", "30 * time.Second"]] },
  );
  test("reads the rows of one module's section, and none of another's or above", () => {
    assert.deepEqual([...defaultsIn(d, "pgstore")], [["pgstore.Config.DisableLeases", "off"]]);
    assert.deepEqual(
      [...defaultsIn(d, "limes")],
      [
        ["limes.VerifierConfig.Leeway", "`DefaultLeeway`"],
        ["limes.DefaultLeeway", "30 * time.Second"],
      ],
    );
  });
  test("reads a field's Default cell, not its prose", () => {
    const reworded = doc({ pgstore: [["pgstore.Config", "DisableLeases", "off", "The store opens leases."]] });
    assert.equal(breaksDefaults(compareDefaults(defaultsIn(d, "pgstore"), defaultsIn(reworded, "pgstore"))), false);
  });
  test("reads no row of a doc older than the Default column, so the first compare with one breaks nothing", () => {
    const old = "## pgstore\n\n| Config | Field | Zero value |\n|---|---|---|\n| `pgstore.Config` | `DisableLeases` | Off: leases open. |\n";
    assert.equal(defaultsIn(old, "pgstore").size, 0);
    assert.equal(breaksDefaults(compareDefaults(defaultsIn(old, "pgstore"), defaultsIn(d, "pgstore"))), false);
  });
  test("reads nothing for a module with no section", () => {
    assert.equal(defaultsIn(d, "biscuit").size, 0);
  });
});

describe("compareDefaults", () => {
  const was = new Map([
    ["c.Kept", "same"],
    ["c.Flipped", "Off."],
    ["c.Gone", "Required."],
  ]);
  const is = new Map([
    ["c.Kept", "same"],
    ["c.Flipped", "On."],
    ["c.New", "Off."],
  ]);
  test("names fields added, removed and doing otherwise at zero", () => {
    const changes = compareDefaults(was, is);
    assert.deepEqual(changes.added, ["c.New"]);
    assert.deepEqual(changes.removed, ["c.Gone"]);
    assert.deepEqual(changes.changed.map(describeDefaultChange), ["`c.Flipped` — Off. → On."]);
    assert.equal(breaksDefaults(changes), true);
  });
  test("a field added breaks no consumer", () => {
    assert.equal(breaksDefaults(compareDefaults(was, new Map([...was, ["c.New", "Off."]]))), false);
    assert.equal(breaksDefaults(compareDefaults(was, was)), false);
  });
});

describe("withDefaultChanges", () => {
  const entry = "## [0.66.0](u) (2026-11-03)\n\n### Features\n\n* a\n";
  test("adds a section naming the fields removed and changed, last", () => {
    assert.equal(
      withDefaultChanges(entry, { added: ["c.New"], removed: ["c.Gone"], changed: [{ field: "c.Flipped", from: "Off.", to: "On." }] }),
      `${entry.trimEnd()}\n\n${DEFAULTS_HEADING}\n\n* removed: \`c.Gone\`\n* changed: \`c.Flipped\` — Off. → On.\n`,
    );
  });
  test("leaves an entry with only fields added as it is", () => {
    assert.equal(withDefaultChanges(entry, { added: ["c.New"], removed: [], changed: [] }), entry);
  });
});

test("defaultChanges compares a module's rows at two revisions, and finds none before the document existed", async () => {
  const repo = await tempRepo();
  try {
    repo.write("README.md", "start");
    await repo.commit("start");
    await repo.run("tag", "v0.1.0");
    repo.write(DEFAULTS_DOC, doc({ limes: [["limes.C", "A", "x"]], pgstore: [["pgstore.Config", "DisableLeases", "off"]] }, { pgstore: [["pgstore.DefaultWatchHeartbeat", "30 * time.Second"]] }));
    await repo.commit("docs");
    await repo.run("tag", "v0.2.0");
    repo.write(DEFAULTS_DOC, doc({ limes: [["limes.C", "A", "y"]], pgstore: [["pgstore.Config", "DisableLeases", "on"]] }, { pgstore: [["pgstore.DefaultWatchHeartbeat", "20 * time.Second"]] }));
    await repo.commit("defaults");
    const pgstore = await defaultChanges(repo.dir, "pgstore", "v0.2.0", "HEAD");
    assert.deepEqual(pgstore.changed.map(describeDefaultChange), [
      "`pgstore.Config.DisableLeases` — off → on",
      "`pgstore.DefaultWatchHeartbeat` — 30 * time.Second → 20 * time.Second",
    ]);
    assert.equal(breaksDefaults(await defaultChanges(repo.dir, "pgstore", "v0.1.0", "HEAD")), false);
  } finally {
    repo.cleanup();
  }
});
