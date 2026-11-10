import assert from "node:assert/strict";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import { git } from "./git.ts";
import { remoteTagCommit, tagPusher } from "./tag-push.ts";
import { tempRepo, type TempRepo } from "./temp-repo.ts";

// TAG is the tag each test pushes.
const TAG = "v1.0.0-rc.1";
// REJECTED is what the remote's pre-receive hook says when it refuses a push.
const REJECTED = "pushes are refused here";

// Remote is a bare repository a checkout pushes to.
interface Remote {
  dir: string;
  cleanup(): void;
}

async function bareRemote(): Promise<Remote> {
  const dir = mkdtempSync(join(tmpdir(), "limes-ci-remote-"));
  await git(["init", "--quiet", "--bare", dir]);
  return { dir, cleanup: () => rmSync(dir, { recursive: true, force: true }) };
}

// withRepos runs body with a checkout holding TAG on its one commit, and an
// empty bare remote.
async function withRepos(body: (repo: TempRepo, remote: Remote, commit: string) => Promise<void>): Promise<void> {
  const repo = await tempRepo();
  const remote = await bareRemote();
  try {
    const commit = await repo.commit("first");
    await repo.run("tag", TAG);
    await body(repo, remote, commit);
  } finally {
    repo.cleanup();
    remote.cleanup();
  }
}

// failingPush runs git as it is, but for a push, which fails as the one
// GitHub answered with a rejection after creating the ref.
function failingPush(args: readonly string[], cwd?: string): Promise<string> {
  if (args[0] === "push") {
    return Promise.reject(new Error(`git push failed: ${REJECTED}`));
  }
  return git(args, cwd);
}

test("tagPusher pushes the tag to the remote", async () => {
  await withRepos(async (repo, remote, commit) => {
    await tagPusher(remote.dir, repo.dir)(TAG);
    assert.equal(await remoteTagCommit(remote.dir, TAG, repo.dir), commit);
  });
});

// The rc.63 run: the push created the tag on the remote and still failed.
test("tagPusher takes a failed push as done when the remote holds the tag at the same commit", async () => {
  await withRepos(async (repo, remote, commit) => {
    await repo.run("push", "--quiet", remote.dir, `refs/tags/${TAG}`);
    await tagPusher(remote.dir, repo.dir, failingPush)(TAG);
    assert.equal(await remoteTagCommit(remote.dir, TAG, repo.dir), commit);
  });
});

test("tagPusher fails when the push fails and the remote has no such tag", async () => {
  await withRepos(async (repo, remote) => {
    await assert.rejects(tagPusher(remote.dir, repo.dir, failingPush)(TAG), new RegExp(REJECTED));
    assert.equal(await remoteTagCommit(remote.dir, TAG, repo.dir), undefined);
  });
});

test("tagPusher fails when the remote holds the tag at another commit", async () => {
  await withRepos(async (repo, remote, commit) => {
    const other = await repo.commit("second");
    await repo.run("push", "--quiet", remote.dir, `${other}:refs/tags/${TAG}`);
    await assert.rejects(tagPusher(remote.dir, repo.dir)(TAG));
    assert.notEqual(await remoteTagCommit(remote.dir, TAG, repo.dir), commit);
  });
});

test("remoteTagCommit reads the commit an annotated tag points at", async () => {
  await withRepos(async (repo, remote, commit) => {
    await repo.run("-c", "user.name=test", "-c", "user.email=test@example.invalid", "tag", "-a", "-f", "-m", "annotated", TAG);
    await repo.run("push", "--quiet", remote.dir, `refs/tags/${TAG}`);
    assert.equal(await remoteTagCommit(remote.dir, TAG, repo.dir), commit);
  });
});
