import assert from "node:assert/strict";
import { devNull } from "node:os";
import { after, before, test } from "node:test";

import { tempRepo, type TempRepo } from "./lib/temp-repo.ts";
import { updateModules } from "./update-modules.ts";

const ROOT = "example.com/root";
const OTHER = "example.com/other";
const DEP = "example.com/dep";
const INDIRECT = "example.com/indirect";

let repo: TempRepo;
before(async () => {
  repo = await tempRepo();
  const mod = (dir: string, path: string, body = "") => repo.write(`${dir}/go.mod`, `module ${path}\n\ngo 1.26\n${body}`);
  mod(".", ROOT, `\nrequire ${DEP} v1.0.0\n`);
  // store replaces the root in and requires dep; app replaces store too, so
  // it comes after store although it sorts first.
  mod("store", `${ROOT}/store`, `\nrequire (\n\t${ROOT} v0.1.0\n\t${DEP} v1.0.0\n\t${INDIRECT} v1.0.0 // indirect\n)\n\nreplace ${ROOT} => ../\n`);
  mod("app", `${ROOT}/app`, `\nrequire (\n\t${ROOT} v0.1.0\n\t${ROOT}/store v0.1.0\n)\n\nreplace (\n\t${ROOT} => ../\n\t${ROOT}/store => ../store\n)\n`);
  // tools requires dep alone, with no replace; pinned replaces dep with a
  // version, which go get -u would not move, so nothing is done to it.
  mod("tools", `${ROOT}/tools`, `\nrequire ${DEP} v1.0.0\n`);
  // linters names a tool, whose requirements are all indirect.
  mod("linters", `${ROOT}/linters`, `\ntool ${OTHER}/cmd/lint\n\nrequire ${OTHER} v1.0.0 // indirect\n`);
  mod("pinned", `${ROOT}/pinned`, `\nrequire ${OTHER} v1.0.0\n\nreplace ${OTHER} => ${OTHER} v0.9.0\n`);
  // empty requires nothing and replaces nothing: there is nothing to do.
  repo.write("empty/go.mod", `module ${ROOT}/empty\n`);
  await repo.run("add", ".");
  // untracked requires dep but git does not track it.
  mod("untracked", `${ROOT}/untracked`, `\nrequire ${DEP} v1.0.0\n`);
});
after(() => repo.cleanup());

test("bumps each nested module's direct requirements, tidies it and builds it, each after those it replaces in", async () => {
  const ran: string[] = [];
  const dirs = await updateModules(repo.dir, async (dir, args) => void ran.push(`${dir}: go ${args.join(" ")}`));
  assert.deepEqual(dirs, ["linters", "tools", "store", "app"]);
  assert.deepEqual(ran, [
    "linters: go get tool",
    "linters: go mod tidy",
    `linters: go build -o ${devNull} tool`,
    `tools: go get -u ${DEP}`,
    "tools: go mod tidy",
    `tools: go build -o ${devNull} ./...`,
    `store: go get -u ${DEP}`,
    "store: go mod tidy",
    `store: go build -o ${devNull} ./...`,
    "app: go mod tidy",
  ]);
});
