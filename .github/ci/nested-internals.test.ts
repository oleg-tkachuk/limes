import assert from "node:assert/strict";
import { copyFileSync, mkdirSync } from "node:fs";
import { join } from "node:path";
import { after, before, describe, test } from "node:test";

import { selectModule } from "./lib/modules.ts";
import { tempRepo, type TempRepo } from "./lib/temp-repo.ts";
import {
  allPassed,
  brokenSymbols,
  checkNested,
  checkRelease,
  failure,
  internalImports,
  LIMES_MODULE,
  nestedSummary,
  Outcome,
  releasesToCheck,
  versionOf,
} from "./nested-internals.ts";

const RELEASE = new URL("../release/", import.meta.url).pathname;
// NESTED is the nested module the repository cases build.
const NESTED = "redisreplay";

// copyTable puts the module table into the repository at dir.
function copyTable(dir: string): void {
  mkdirSync(join(dir, ".github/release"), { recursive: true });
  copyFileSync(join(RELEASE, "modules.cjs"), join(dir, ".github/release/modules.cjs"));
}

describe("versionOf", () => {
  test("reads a release's version under its prefix", () => {
    assert.deepEqual(versionOf("exchange/", "exchange/v1.2.3"), { tag: "exchange/v1.2.3", major: 1, minor: 2, patch: 3 });
    assert.deepEqual(versionOf("", "v0.10.0"), { tag: "v0.10.0", major: 0, minor: 10, patch: 0 });
  });

  test("is no version for a prerelease, another module's tag, or another shape", () => {
    for (const tag of ["exchange/v1.0.0-rc.1", "pgstore/v1.0.0", "exchange/v1.0", "exchange/latest"]) {
      assert.equal(versionOf("exchange/", tag), undefined, tag);
    }
  });
});

describe("releasesToCheck", () => {
  test("is the newest release alone before v1, by version, not by name", () => {
    assert.deepEqual(releasesToCheck("exchange/", ["exchange/v0.9.0", "exchange/v0.17.0", "exchange/v0.2.0", "exchange/v0.18.0-rc.1"]), [
      "exchange/v0.17.0",
    ]);
  });

  test("is every release of the newest's major from v1 on, newest first", () => {
    const tags = ["exchange/v0.17.0", "exchange/v1.0.0", "exchange/v1.10.0", "exchange/v1.2.1", "exchange/v1.11.0-rc.1"];
    assert.deepEqual(releasesToCheck("exchange/", tags), ["exchange/v1.10.0", "exchange/v1.2.1", "exchange/v1.0.0"]);
    assert.deepEqual(releasesToCheck("exchange/", [...tags, "exchange/v2.0.0"]), ["exchange/v2.0.0"]);
  });

  test("is none before any release", () => {
    assert.deepEqual(releasesToCheck("biscuit/", ["exchange/v0.1.0", "biscuit/v0.1.0-rc.1"]), []);
  });
});

describe("internalImports", () => {
  test("names each internal package imported, once, and no other", () => {
    const source = `package exchange

import (
	"errors"

	"${LIMES_MODULE}"
	"${LIMES_MODULE}/dpop"
	"${LIMES_MODULE}/internal/errcode"
	"${LIMES_MODULE}/internal/errs"
	jose "${LIMES_MODULE}/internal/jose"
)

var _ = "${LIMES_MODULE}/internal/errs"
`;
    assert.deepEqual(internalImports(source), [`${LIMES_MODULE}/internal/errcode`, `${LIMES_MODULE}/internal/errs`, `${LIMES_MODULE}/internal/jose`]);
  });
});

describe("brokenSymbols", () => {
  const imports = [`${LIMES_MODULE}/internal/errs`, `${LIMES_MODULE}/internal/errcode`];

  test("names each symbol of an internal package the compiler names", () => {
    const output = `# ${LIMES_MODULE}/exchange
./exchange.go:12:10: undefined: errs.Errorf
./exchange.go:40:2: cannot use (*codedError)(nil) as errcode.Coded value: missing method LimesErrorCode
./handler.go:3:3: undefined: errs.Errorf
./narrow.go:9:9: undefined: fmt.Sprint`;
    assert.deepEqual(brokenSymbols(output, imports), ["errcode.Coded", "errs.Errorf"]);
  });

  test("names a package gone by its path", () => {
    const output = `exchange.go:9:2: package ${LIMES_MODULE}/internal/errs is not in std`;
    assert.deepEqual(brokenSymbols(output, imports), [`${LIMES_MODULE}/internal/errs`]);
  });

  test("names nothing the compiler did not blame on an internal package", () => {
    assert.deepEqual(brokenSymbols("./x.go:1:1: undefined: limes.ErrGone", imports), []);
  });
});

describe("failure", () => {
  test("names the module, the tag, the command and the symbols", () => {
    const v = failure("exchange", "exchange/v1.0.0", ["build", "./..."], "./e.go:1:1: undefined: errs.Errorf\n", [`${LIMES_MODULE}/internal/errs`], true);
    assert.equal(v.ok, false);
    assert.match(v.message, /^exchange: exchange\/v1\.0\.0 no longer passes go build \.\/\.\.\. against this limes — the internal symbols it uses: errs\.Errorf\n/);
    assert.match(v.message, /undefined: errs\.Errorf$/);
    assert.match(failure("exchange", "exchange/v1.0.0", ["vet", "./..."], "boom", [], true).message, /no internal symbol named/);
  });

  test("fails a stable release on an exported break", () => {
    assert.equal(failure("pgstore", "pgstore/v1.0.0", ["build", "./..."], "./w.go:1:1: undefined: limes.Gone", [], true).ok, false);
  });

  test("passes a release before v1 that only an exported break refuses", () => {
    const v = failure("pgstore", "pgstore/v0.67.0", ["build", "./..."], "./w.go:1:1: not enough arguments in call to c.Listening", [], false);
    assert.equal(v.ok, true);
    assert.match(v.message, /allowed before v1/);
  });

  test("fails a release before v1 on an internal break", () => {
    const v = failure("exchange", "exchange/v0.18.0", ["build", "./..."], "./e.go:1:1: undefined: errs.Errorf", [`${LIMES_MODULE}/internal/errs`], false);
    assert.equal(v.ok, false);
  });
});

describe("checkNested", () => {
  let repo: TempRepo;
  before(async () => {
    repo = await tempRepo();
    copyTable(repo.dir);
    await repo.commit("build: add the module table");
    for (const tag of ["v0.5.0", "redisreplay/v0.1.0", "redisreplay/v0.2.0", "redisreplay/v0.3.0-rc.1", "pgstore/v0.9.0", "pgstore/v1.0.0", "pgstore/v1.1.0"]) {
      await repo.run("tag", tag);
    }
  });
  after(() => repo.cleanup());

  test("checks each nested module's releases to check, skips one never released, and fails with any", async () => {
    const calls: string[] = [];
    const results = await checkNested(repo.dir, async (module, tag) => {
      calls.push(tag);
      return { ok: tag !== "pgstore/v1.0.0", message: `${module.name} ${tag}` };
    });
    assert.deepEqual(calls, ["redisreplay/v0.2.0", "pgstore/v1.1.0", "pgstore/v1.0.0"]);
    assert.deepEqual(
      results.map((r) => `${r.module} ${r.outcome}`),
      ["redisreplay passed", "pgstore failed", "otelmetric skipped", "exchange skipped", "biscuit skipped"],
    );
    assert.equal(results.find((r) => r.module === "pgstore")?.message, "pgstore pgstore/v1.0.0");
    assert.equal(allPassed(results), false);
    assert.equal(allPassed(results.filter((r) => r.outcome !== Outcome.Failed)), true);
    assert.match(nestedSummary(results), /^### Nested releases against limes\n\n\| Module \| Result \| Detail \|/);
  });
});

describe("checkRelease", () => {
  let repo: TempRepo;
  const tag = `${NESTED}/v0.1.0`;
  before(async () => {
    repo = await tempRepo();
    copyTable(repo.dir);
    repo.write("go.mod", `module ${LIMES_MODULE}\n\ngo 1.24\n`);
    repo.write("internal/errs/errs.go", "package errs\n\n// Request is kept.\nfunc Request() error { return nil }\n\n// TestOnly is a helper of the nested module's tests.\nfunc TestOnly() {}\n");
    repo.write(
      `${NESTED}/go.mod`,
      `module ${LIMES_MODULE}/${NESTED}\n\ngo 1.24\n\nrequire ${LIMES_MODULE} v0.0.0\n\nreplace ${LIMES_MODULE} => ../\n`,
    );
    repo.write(`${NESTED}/r.go`, `package ${NESTED}\n\nimport "${LIMES_MODULE}/internal/errs"\n\n// Check is errs.Request.\nfunc Check() error { return errs.Request() }\n`);
    repo.write(`${NESTED}/r_test.go`, `package ${NESTED}\n\nimport "${LIMES_MODULE}/internal/errs"\n\nvar _ = errs.TestOnly\n`);
    await repo.commit("feat: start");
    await repo.run("tag", tag);
  });
  after(() => repo.cleanup());

  test("passes a release that builds against limes as it is, its tests left out", async () => {
    repo.write("internal/errs/errs.go", "package errs\n\n// Request is kept.\nfunc Request() error { return nil }\n");
    await repo.commit("refactor: drop a test helper");
    const v = await checkRelease(repo.dir, selectModule(repo.dir, NESTED), tag);
    assert.equal(v.ok, true, v.message);
  });

  test("fails a release whose internal symbol limes no longer has, naming it", async () => {
    repo.write("internal/errs/errs.go", "package errs\n\n// Requested replaced Request.\nfunc Requested() error { return nil }\n");
    await repo.commit("refactor!: rename Request");
    const v = await checkRelease(repo.dir, selectModule(repo.dir, NESTED), tag);
    assert.equal(v.ok, false);
    assert.match(v.message, new RegExp(`^${NESTED}: ${tag} no longer passes go build \\./\\.\\.\\. against this limes — the internal symbols it uses: errs\\.Request\\n`));
  });
});
