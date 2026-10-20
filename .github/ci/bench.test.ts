import assert from "node:assert/strict";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, test } from "node:test";

import { BENCH_FILE, PGSTORE, benchArgs, compareBenchmarks, newestRelease, report, type Bench } from "./bench.ts";

const HEAD_OUT = "BenchmarkCharge 100 3000 ns/op (head)";
const BASE_OUT = "BenchmarkCharge 100 2900 ns/op (base)";
const BENCH_SOURCE = "package pgstore // benchmarks of this commit\n";

describe("newestRelease", () => {
  test("the newest stable pgstore tag", () => assert.equal(newestRelease(["pgstore/v0.31.0-rc.1", "pgstore/v0.30.1", "pgstore/v0.30.0"]), "pgstore/v0.30.1"));
  test("no other module's tag", () => assert.equal(newestRelease(["v0.109.0", "redisreplay/v0.1.55"]), undefined));
  test("none", () => assert.equal(newestRelease([]), undefined));
});

test("the go command runs benchmarks only", () => {
  const args = benchArgs();
  assert.deepEqual(args.slice(0, 5), ["test", "-run", "^$", "-bench", "."]);
  assert.ok(args.includes("-count") && args.includes("-timeout"));
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
    mkdirSync(join(root, PGSTORE));
    mkdirSync(join(worktree, PGSTORE));
    writeFileSync(join(root, BENCH_FILE), BENCH_SOURCE);
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

  test("this commit against the newest release, the release first", async () => {
    const c = await compareBenchmarks(fake(), root);
    assert.deepEqual(c, { base: "pgstore/v0.30.1", baseProblem: undefined, table: `${BASE_OUT} | ${HEAD_OUT}` });
    assert.equal(readFileSync(join(worktree, BENCH_FILE), "utf8"), BENCH_SOURCE, "the benchmarks were not copied into the release");
    assert.deepEqual(calls, ["run head", "checkout pgstore/v0.30.1", "run base", "benchstat pgstore/v0.30.1 HEAD", "remove"]);
  });

  test("a release the benchmarks do not run on is said so, and this commit is reported alone", async () => {
    baseFails = true;
    const c = await compareBenchmarks(fake(), root);
    assert.equal(c.table, HEAD_OUT);
    assert.match(c.baseProblem ?? "", /did not run there: go test failed: undefined: newHelper$/);
    assert.ok(calls.includes("remove"), "the release's worktree was left behind");
  });

  test("with no release, this commit alone", async () => {
    tags = [];
    const c = await compareBenchmarks(fake(), root);
    assert.deepEqual(c, { base: undefined, baseProblem: undefined, table: HEAD_OUT });
    assert.ok(!calls.some((c) => c.startsWith("checkout")));
  });

  test("this commit's benchmarks failing fails the run", async () => {
    const b = fake();
    b.run = async () => {
      throw new Error("broken");
    };
    await assert.rejects(compareBenchmarks(b, root), /broken/);
  });
});

describe("report", () => {
  test("a comparison names the release", () =>
    assert.match(report({ base: "pgstore/v0.30.1", baseProblem: undefined, table: "t" }), /against `pgstore\/v0\.30\.1`[\s\S]*```text\nt\n```/));
  test("a release not comparable says why", () =>
    assert.match(report({ base: "pgstore/v0.30.1", baseProblem: "it broke", table: "t" }), /`pgstore\/v0\.30\.1` is not comparable \(it broke\)/));
  test("no release", () => assert.match(report({ base: undefined, baseProblem: undefined, table: "t" }), /No pgstore release/));
});

test("the benchmark file exists where it is copied from", () => {
  assert.ok(existsSync(join(import.meta.dirname, "..", "..", BENCH_FILE)));
});
