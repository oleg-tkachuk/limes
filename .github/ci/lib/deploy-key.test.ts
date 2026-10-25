import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, rmSync, statSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, test } from "node:test";

import { GITHUB_HOST_KEY, GITHUB_SSH_HOST, pushUrl, useDeployKey } from "./deploy-key.ts";

// KEY stands for a private key's text; ssh never reads it here.
const KEY = "-----BEGIN OPENSSH PRIVATE KEY-----\nnot a key\n-----END OPENSSH PRIVATE KEY-----";
// OWNER_ONLY is the mode ssh asks of a private key's file.
const OWNER_ONLY = 0o600;
// PERMISSION_BITS masks a file's mode to its permissions.
const PERMISSION_BITS = 0o777;

describe("useDeployKey", () => {
  let dir: string;
  beforeEach(() => {
    dir = mkdtempSync(join(tmpdir(), "limes-ssh-"));
  });
  afterEach(() => rmSync(dir, { recursive: true, force: true }));

  test("writes the key for its owner alone and pins GitHub's host key", () => {
    const command = useDeployKey(KEY, dir);
    const keyFile = join(dir, "deploy_key");
    assert.equal(readFileSync(keyFile, "utf8"), `${KEY}\n`);
    assert.equal(statSync(keyFile).mode & PERMISSION_BITS, OWNER_ONLY);
    assert.equal(readFileSync(join(dir, "known_hosts"), "utf8"), `${GITHUB_SSH_HOST} ${GITHUB_HOST_KEY}\n`);
    for (const option of [`-i ${keyFile}`, "IdentitiesOnly=yes", "IdentityAgent=none", `UserKnownHostsFile=${join(dir, "known_hosts")}`, "StrictHostKeyChecking=yes"]) {
      assert.ok(command.includes(option), `${command} lacks ${option}`);
    }
  });
  test("refuses an empty key", () => assert.throws(() => useDeployKey(" \n", dir), /empty/));
  test("refuses a directory the command would split", () => assert.throws(() => useDeployKey(KEY, join(dir, "a b")), /white space/));
});

test("pushUrl is the repository's SSH URL on GitHub", () => assert.equal(pushUrl("o/r"), "git@github.com:o/r.git"));
