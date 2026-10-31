// The release pushes its tags over SSH with a deploy key: on a repository
// outside an organisation, a deploy key is the one actor a ruleset can let
// create a release tag while every person and token is refused
// (repo-settings.ts, tagCreationRuleset).

import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import { requireEnv } from "./env.ts";

// DEPLOY_KEY_ENV carries the deploy key's private half, from the
// RELEASE_DEPLOY_KEY secret (release.yaml).
export const DEPLOY_KEY_ENV = "RELEASE_DEPLOY_KEY";
// GITHUB_SSH_HOST is the host a tag is pushed to, and GITHUB_HOST_KEY its
// ed25519 host key as GitHub publishes it (the ssh_keys of
// https://api.github.com/meta): pinned, so a push never trusts the first
// host that answers.
export const GITHUB_SSH_HOST = "github.com";
export const GITHUB_HOST_KEY = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl";
// PRIVATE_KEY_MODE is what ssh asks of a private key's file: the owner's
// to read alone.
const PRIVATE_KEY_MODE = 0o600;
// The files useDeployKey writes, in the directory it is given.
const KEY_FILE = "deploy_key";
const KNOWN_HOSTS_FILE = "known_hosts";

// pushUrl is the SSH URL the release pushes repository's tags to.
export function pushUrl(repository: string): string {
  return `git@${GITHUB_SSH_HOST}:${repository}.git`;
}

// useDeployKey writes key and the pinned host key into dir, a directory of
// the run's own, and returns the GIT_SSH_COMMAND that pushes with that key
// alone: no agent's, no other file's, and to no host but GitHub's.
export function useDeployKey(key: string, dir: string): string {
  if (key.trim() === "") {
    throw new Error(`${DEPLOY_KEY_ENV} is empty`);
  }
  if (/\s/.test(dir)) {
    throw new Error(`${dir}: a key directory with white space would split the ssh command`);
  }
  const keyFile = join(dir, KEY_FILE);
  writeFileSync(keyFile, key.endsWith("\n") ? key : `${key}\n`, { mode: PRIVATE_KEY_MODE });
  const knownHosts = join(dir, KNOWN_HOSTS_FILE);
  writeFileSync(knownHosts, `${GITHUB_SSH_HOST} ${GITHUB_HOST_KEY}\n`);
  return [
    "ssh",
    "-i",
    keyFile,
    "-o",
    "IdentitiesOnly=yes",
    "-o",
    "IdentityAgent=none",
    "-o",
    `UserKnownHostsFile=${knownHosts}`,
    "-o",
    "StrictHostKeyChecking=yes",
  ].join(" ");
}

// SSH_DIR_PREFIX names the directory the deploy key is written to, under
// the runner's temporary directory.
const SSH_DIR_PREFIX = "release-ssh-";

// withDeployKey runs fn with every push over SSH with the deploy key, which
// alone may create a release tag (repo-settings.ts), handing it repository's
// push URL. The key's text leaves the environment before any child process
// starts, and its file is removed once fn is done.
export async function withDeployKey<T>(repository: string, fn: (push: string) => Promise<T>): Promise<T> {
  const sshDir = mkdtempSync(join(requireEnv("RUNNER_TEMP"), SSH_DIR_PREFIX));
  process.env.GIT_SSH_COMMAND = useDeployKey(requireEnv(DEPLOY_KEY_ENV), sshDir);
  delete process.env[DEPLOY_KEY_ENV];
  try {
    return await fn(pushUrl(repository));
  } finally {
    rmSync(sshDir, { recursive: true, force: true });
  }
}
