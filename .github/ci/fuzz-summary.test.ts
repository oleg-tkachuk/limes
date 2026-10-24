import assert from "node:assert/strict";
import { describe, test } from "node:test";

import { CANCELLED, SUCCESS, fuzzSummary, parseFuzzLog } from "./fuzz-summary.ts";

const PKG = "github.com/oleg-tkachuk/limes/internal/ttlcache";
const TARGET = "FuzzCounters";

// The logs go printed, as fuzz.yaml writes them.
const PASSED = `fuzz: elapsed: 0s, gathering baseline coverage: 0/118 completed
fuzz: elapsed: 0s, gathering baseline coverage: 118/118 completed, now fuzzing with 4 workers
fuzz: elapsed: 3s, execs: 1581201 (527061/sec), new interesting: 2 (total: 120)
fuzz: elapsed: 10m0s, execs: 316181844 (526969/sec), new interesting: 14 (total: 132)
PASS
ok  	${PKG}	600.214s
`;
const FAILED = `fuzz: elapsed: 0s, gathering baseline coverage: 0/1 completed
fuzz: elapsed: 0s, gathering baseline coverage: 1/1 completed, now fuzzing with 14 workers
fuzz: elapsed: 3s, execs: 905 (301/sec), new interesting: 1 (total: 2)
fuzz: minimizing 44-byte failing input file
fuzz: elapsed: 3s, minimizing
--- FAIL: FuzzCounters (3.10s)
    --- FAIL: FuzzCounters (0.00s)
        fuzz_test.go:9: found

    Failing input written to testdata/fuzz/FuzzCounters/310637d0c4244160
    To re-run:
    go test -run=FuzzCounters/310637d0c4244160
FAIL
exit status 1
FAIL	${PKG}	3.600s
`;
const SEED_FAILED = `--- FAIL: FuzzCounters (0.00s)
    --- FAIL: FuzzCounters/seed#3 (0.00s)
        fuzz_test.go:146: step 2: counted hits 1
FAIL
`;

describe("parseFuzzLog", () => {
  test("the last progress line of a search", () =>
    assert.deepEqual(parseFuzzLog(PASSED), { elapsed: "10m0s", executions: 316181844, interesting: 14 }));
  test("the failing input and how to run it again", () =>
    assert.deepEqual(parseFuzzLog(FAILED), {
      elapsed: "3s",
      executions: 905,
      interesting: 1,
      input: "testdata/fuzz/FuzzCounters/310637d0c4244160",
      rerun: "go test -run=FuzzCounters/310637d0c4244160",
    }));
  test("a seed that failed before any search", () => assert.deepEqual(parseFuzzLog(SEED_FAILED), {}));
});

describe("fuzzSummary", () => {
  test("a search that passed", () => {
    const page = fuzzSummary(PKG, TARGET, SUCCESS, parseFuzzLog(PASSED));
    assert.match(page, /^### Fuzz FuzzCounters\n/);
    assert.match(page, /\| 10m0s \| 316181844 \| 14 \| passed \|/);
    assert.doesNotMatch(page, /run it again/);
  });
  test("a failing input, with the command that runs it again in its package", () => {
    const page = fuzzSummary(PKG, TARGET, "failure", parseFuzzLog(FAILED));
    assert.match(page, /\| failed \|/);
    assert.match(page, /`testdata\/fuzz\/FuzzCounters\/310637d0c4244160`/);
    assert.match(page, /artifact `fuzz-FuzzCounters`/);
    assert.match(page, new RegExp(`\`\`\`bash\\ngo test -run=FuzzCounters/310637d0c4244160 ${PKG.replaceAll(".", "\\.")}\\n\`\`\``));
  });
  test("a cancelled search: neither passed nor failed, and no failure to explain", () => {
    const page = fuzzSummary(PKG, TARGET, CANCELLED, parseFuzzLog(PASSED));
    assert.match(page, /\| cancelled \|/);
    assert.doesNotMatch(page, /failed|before its search/);
  });
  test("a failure before the search: no input to name", () => {
    const page = fuzzSummary(PKG, TARGET, "failure", parseFuzzLog(SEED_FAILED));
    assert.match(page, /\| — \| — \| — \| failed \|/);
    assert.match(page, /before its search wrote an input/);
  });
});
