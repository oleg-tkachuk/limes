import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, before, describe, test } from "node:test";

import { LIMES_MODULE, buildsOnRequired } from "./go-modules.ts";

// OFFLINE keeps go from the network, so a limes it would fetch is missing
// as an unreleased one is.
const OFFLINE: Record<string, string> = { GOPROXY: "off", GOWORK: "off", GOTOOLCHAIN: "local", GOFLAGS: "" };

// REQUIRED is the limes the nested module requires, which no proxy holds.
const REQUIRED = "v0.1.0";

// NESTED is the nested module's directory under the repository.
const NESTED = "nested";

describe("buildsOnRequired", () => {
  let work: string;
  const saved: Record<string, string | undefined> = {};
  before(() => {
    work = mkdtempSync(join(tmpdir(), "limes-go-modules-"));
    for (const [k, v] of Object.entries(OFFLINE)) {
      saved[k] = process.env[k];
      process.env[k] = v;
    }
  });
  after(() => {
    rmSync(work, { recursive: true, force: true });
    for (const [k, v] of Object.entries(saved)) {
      if (v === undefined) delete process.env[k];
      else process.env[k] = v;
    }
  });

  test("a module that needs no limes builds", async () => {
    const dir = mkdtempSync(join(work, "plain-"));
    writeFileSync(join(dir, "go.mod"), "module example.com/plain\n\ngo 1.21\n");
    writeFileSync(join(dir, "plain.go"), "package plain\n");
    assert.deepEqual(await buildsOnRequired(dir), { builds: true, output: "" });
  });

  test("a module that builds only through its replace of limes does not build on what it requires, and its go.mod is left as it was", async () => {
    const root = mkdtempSync(join(work, "repo-"));
    writeFileSync(join(root, "go.mod"), `module ${LIMES_MODULE}\n\ngo 1.21\n`);
    writeFileSync(join(root, "limes.go"), "package limes\n\nconst New = 1\n");
    const dir = join(root, NESTED);
    mkdirSync(dir);
    const goMod = `module ${LIMES_MODULE}/${NESTED}\n\ngo 1.21\n\nrequire ${LIMES_MODULE} ${REQUIRED}\n\nreplace ${LIMES_MODULE} => ../\n`;
    writeFileSync(join(dir, "go.mod"), goMod);
    writeFileSync(join(dir, `${NESTED}.go`), `package ${NESTED}\n\nimport "${LIMES_MODULE}"\n\nvar _ = limes.New\n`);
    const build = await buildsOnRequired(dir);
    assert.equal(build.builds, false);
    // go names the module it could not fetch as path@version or path version.
    assert.match(build.output, new RegExp(`${LIMES_MODULE}[@ ]${REQUIRED}`));
    assert.equal(readFileSync(join(dir, "go.mod"), "utf8"), goMod);
  });
});
