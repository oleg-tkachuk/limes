import assert from "node:assert/strict";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, test } from "node:test";

import { parse } from "yaml";

import { BENCHED, BENCH_ROUNDS, benchArgs, benchedName, benchFailure, benchFile, compareBenchmarks, newestRelease, report, tail, type Bench, type Benched } from "./bench.ts";
import { selectModule } from "./lib/modules.ts";

// PGSTORE is a nested module as the comparison sees it.
const PGSTORE: Benched = { dir: "pgstore", prefix: "pgstore/" };
const ROOT_DIR = join(import.meta.dirname, "..", "..");

const HEAD_OUT = "BenchmarkCharge 100 3000 ns/op (head)";
const BASE_OUT = "BenchmarkCharge 100 2900 ns/op (base)";
const BENCH_SOURCE = "package pgstore // benchmarks of this commit\n";

describe("newestRelease", () => {
  test("the newest stable pgstore tag", () =>
    assert.equal(newestRelease(["pgstore/v0.31.0-rc.1", "pgstore/v0.30.1", "pgstore/v0.30.0"], PGSTORE.prefix), "pgstore/v0.30.1"));
  test("no other module's tag", () => assert.equal(newestRelease(["v0.109.0", "redisreplay/v0.1.55"], PGSTORE.prefix), undefined));
  test("the root module's, past a candidate and the nested modules'", () =>
    assert.equal(newestRelease(["v1.0.0-rc.1", "pgstore/v0.30.2", "v0.109.0"], ""), "v0.109.0"));
  test("none", () => assert.equal(newestRelease([], PGSTORE.prefix), undefined));
});

test("the go command runs benchmarks only", () => {
  const args = benchArgs();
  assert.deepEqual(args.slice(0, 5), ["test", "-run", "^$", "-bench", "."]);
  // One round a call: the rounds of the two sides take turns.
  assert.deepEqual(args.slice(args.indexOf("-count"), args.indexOf("-count") + 2), ["-count", "1"]);
  assert.ok(args.includes("-timeout"));
});

describe("compareBenchmarks", () => {
  let root: string;
  let worktree: string;
  let calls: string[];
  let tags: string[];
  let baseFails: boolean;
  beforeEach(() => {
    root = mkdtempSync(join(tmpdir(), "limes-bench-root-"));
    worktree = mkdtempSync(join(tmpdir(), "limes-bench-base-"));
    mkdirSync(join(root, PGSTORE.dir));
    mkdirSync(join(worktree, PGSTORE.dir));
    writeFileSync(join(root, benchFile(PGSTORE)), BENCH_SOURCE);
    calls = [];
    tags = ["pgstore/v0.30.1"];
    baseFails = false;
  });
  afterEach(() => {
    rmSync(root, { recursive: true, force: true });
    rmSync(worktree, { recursive: true, force: true });
  });

  function fake(): Bench {
    return {
      tags: async () => tags,
      run: async (dir) => {
        calls.push(`run ${dir === root ? "head" : "base"}`);
        if (dir !== root && baseFails) {
          throw new Error("go test failed: undefined: newHelper\nmore");
        }
        return dir === root ? HEAD_OUT : BASE_OUT;
      },
      checkout: async (tag) => {
        calls.push(`checkout ${tag}`);
        return worktree;
      },
      remove: async () => void calls.push("remove"),
      benchstat: async (outputs, labels) => {
        calls.push(`benchstat ${labels.join(" ")}`);
        return outputs.join(" | ");
      },
    };
  }

  test("this commit against the newest release, the release first, in rounds that take turns", async () => {
    const c = await compareBenchmarks(fake(), root, PGSTORE);
    assert.deepEqual(c, { base: "pgstore/v0.30.1", baseProblem: undefined, table: `${BASE_OUT.repeat(BENCH_ROUNDS)} | ${HEAD_OUT.repeat(BENCH_ROUNDS)}` });
    assert.equal(readFileSync(join(worktree, benchFile(PGSTORE)), "utf8"), BENCH_SOURCE, "the benchmarks were not copied into the release");
    const turns = Array.from({ length: BENCH_ROUNDS }, () => ["run head", "run base"]).flat();
    assert.deepEqual(calls, ["checkout pgstore/v0.30.1", ...turns, "benchstat pgstore/v0.30.1 HEAD", "remove"]);
  });

  test("a release the benchmarks do not run on is said so, and this commit is reported alone", async () => {
    baseFails = true;
    const c = await compareBenchmarks(fake(), root, PGSTORE);
    assert.equal(c.table, HEAD_OUT.repeat(BENCH_ROUNDS), "this commit's rounds are not all reported");
    assert.match(c.baseProblem ?? "", /did not run there: go test failed: undefined: newHelper$/);
    assert.ok(calls.includes("remove"), "the release's worktree was left behind");
  });

  // The root module's benchmarks sit at the root of the release's worktree.
  test("the root module's benchmarks are copied to the release's root", async () => {
    writeFileSync(join(root, benchFile({ dir: "" })), BENCH_SOURCE);
    tags = ["v0.109.0"];
    const c = await compareBenchmarks(fake(), root, { dir: "", prefix: "" });
    assert.equal(c.base, "v0.109.0");
    assert.equal(readFileSync(join(worktree, benchFile({ dir: "" })), "utf8"), BENCH_SOURCE);
  });

  test("with no release, this commit alone", async () => {
    tags = [];
    const c = await compareBenchmarks(fake(), root, PGSTORE);
    assert.deepEqual(c, { base: undefined, baseProblem: undefined, table: HEAD_OUT.repeat(BENCH_ROUNDS) });
    assert.ok(!calls.some((c) => c.startsWith("checkout")));
  });

  test("this commit's benchmarks failing fails the run", async () => {
    const b = fake();
    b.run = async () => {
      throw new Error("broken");
    };
    await assert.rejects(compareBenchmarks(b, root, PGSTORE), /broken/);
  });
});

describe("report", () => {
  test("a comparison names the release", () =>
    assert.match(report("pgstore", { base: "pgstore/v0.30.1", baseProblem: undefined, table: "t" }), /against `pgstore\/v0\.30\.1`[\s\S]*```text\nt\n```/));
  test("a release not comparable says why", () =>
    assert.match(report("pgstore", { base: "pgstore/v0.30.1", baseProblem: "it broke", table: "t" }), /`pgstore\/v0\.30\.1` is not comparable \(it broke\)/));
  test("no release", () => assert.match(report("pgstore", { base: undefined, baseProblem: undefined, table: "t" }), /### pgstore benchmarks[\s\S]*No pgstore release/));
});

test("every benched module has its benchmarks where they are copied from", () => {
  for (const name of BENCHED) {
    assert.ok(existsSync(join(ROOT_DIR, benchFile(selectModule(ROOT_DIR, name)))), `${name} has no ${benchFile(selectModule(ROOT_DIR, name))}`);
  }
});

// The weekly workflow benchmarks each module bench.ts benches, and no other.
test("bench.yaml's matrix is the benched modules", () => {
  const workflow = parse(readFileSync(join(ROOT_DIR, ".github/workflows/bench.yaml"), "utf8")) as {
    jobs: { bench: { strategy: { matrix: { module: string[] } } } };
  };
  assert.deepEqual([...workflow.jobs.bench.strategy.matrix.module].sort(), [...BENCHED].sort());
});

test("a failed run's error carries the end of its output", () => {
  const output = Array.from({ length: 30 }, (_, i) => `line ${i}`).join("\n");
  const end = tail(`${output}\n`).split("\n");
  assert.equal(end.length, 20);
  assert.equal(end.at(-1), "line 29");
});

describe("benchedName", () => {
  test("one benched module", () => assert.equal(benchedName([BENCHED[0] ?? ""]), BENCHED[0]));
  for (const args of [[], ["exchange"], [BENCHED[0] ?? "", "more"]]) {
    test(`refuses ${JSON.stringify(args)}`, () => assert.throws(() => benchedName(args), /usage: bench\.ts/));
  }
});

describe("benchFailure", () => {
  test("carries the tail of what go test printed", () => {
    const failed = new Error("exit status 1", { cause: { stdout: "--- FAIL: BenchmarkVerify\nFAIL" } });
    const err = benchFailure(failed);
    assert.match(err.message, /^exit status 1\n/);
    assert.match(err.message, /--- FAIL: BenchmarkVerify/);
    assert.equal(err.cause, failed);
  });
  test("says only what failed when nothing was printed", () => {
    assert.equal(benchFailure(new Error("not found")).message, "not found\n");
    assert.equal(benchFailure("refused").message, "refused\n");
  });
});
