import assert from "node:assert/strict";
import { after, before, test } from "node:test";

import { tempRepo, type TempRepo } from "./lib/temp-repo.ts";
import { tidyReplacingModules } from "./tidy-modules.ts";

const ROOT = "example.com/root";
const OTHER = "example.com/other";

let repo: TempRepo;
before(async () => {
  repo = await tempRepo();
  const mod = (dir: string, path: string, body = "") => repo.write(`${dir}/go.mod`, `module ${path}\n\ngo 1.26\n${body}`);
  mod(".", ROOT);
  // store and the "space dir" replace the root in; app replaces store too,
  // so it comes after store although it sorts first.
  mod("store", `${ROOT}/store`, `\nrequire ${ROOT} v0.1.0\n\nreplace ${ROOT} => ../\n`);
  mod("app", `${ROOT}/app`, `\nrequire (\n\t${ROOT} v0.1.0\n\t${ROOT}/store v0.1.0\n)\n\nreplace (\n\t${ROOT} => ../\n\t${ROOT}/store => ../store\n)\n`);
  mod("space dir", `${ROOT}/spaced`, `\nreplace ${ROOT} => ../\n`);
  // released requires the root by version; vendored replaces it with
  // another version, not a directory; tools replaces another module in.
  mod("released", `${ROOT}/released`, `\nrequire ${ROOT} v0.1.0\n`);
  mod("vendored", `${ROOT}/vendored`, `\nreplace ${ROOT} => ${ROOT} v0.0.9\n`);
  mod("tools", `${ROOT}/tools`, `\nreplace ${OTHER} => ../other\n`);
  await repo.run("add", ".");
  // untracked replaces the root in but git does not track it.
  mod("untracked", `${ROOT}/untracked`, `\nreplace ${ROOT} => ../\n`);
});
after(() => repo.cleanup());

test("tidies exactly the modules replacing the root in, each after those it replaces in", async () => {
  const tidied: string[] = [];
  await tidyReplacingModules(repo.dir, async (dir) => void tidied.push(dir));
  assert.deepEqual(tidied, ["space dir", "store", "app"]);
});

test("with no module replacing the root in, tidies nothing", async () => {
  await repo.run("rm", "-q", "-r", "--cached", "store", "app", "space dir");
  const tidied: string[] = [];
  await tidyReplacingModules(repo.dir, async (dir) => void tidied.push(dir));
  assert.deepEqual(tidied, []);
});
