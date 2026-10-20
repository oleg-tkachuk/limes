// A module's release tags, and their GitHub releases.

import { readFileSync } from "node:fs";

import { info, warning } from "./actions.ts";
import { git, lines } from "./git.ts";
import type { GitHub } from "./github.ts";

// RECENT_RELEASE_TAGS is how far back a release left behind is looked for:
// one failed publish is the case, a few in a row are covered, and every tag
// looked at costs an API call on each release.
const RECENT_RELEASE_TAGS = 10;

// isReleaseTag reports whether tag is a release tag of the module whose tags
// start with prefix: the prefix as it is spelt, then v and a digit.
export function isReleaseTag(tag: string, prefix: string): boolean {
  return tag.startsWith(`${prefix}v`) && /^[0-9]/.test(tag.slice(prefix.length + 1));
}

// isStableReleaseTag reports whether tag is a release tag of the module
// whose tags start with prefix that names no prerelease: no -rc.N, nothing
// after the patch number.
export function isStableReleaseTag(tag: string, prefix: string): boolean {
  return isReleaseTag(tag, prefix) && !tag.slice(prefix.length).includes("-");
}

// tagsOnHead is every tag on the checked-out commit.
export async function tagsOnHead(cwd?: string): Promise<string[]> {
  return lines(await git(["tag", "--points-at", "HEAD"], cwd));
}

// newReleaseTag is the module's release tag this run pushed — on HEAD and
// not in before — or, when it pushed none, one on HEAD with no GitHub
// release, which an earlier run pushed and failed to publish. Undefined when
// there is neither: no release was due. A prerelease tag is never one: it
// is published by the run that cuts it, as a prerelease, and taken here it
// would be published as a release and marked Latest.
export async function newReleaseTag(github: GitHub, before: readonly string[], prefix: string, cwd?: string): Promise<string | undefined> {
  const onHead = (await tagsOnHead(cwd)).filter((t) => isStableReleaseTag(t, prefix));
  const pushed = onHead.find((t) => !before.includes(t));
  if (pushed !== undefined) {
    return pushed;
  }
  for (const tag of onHead) {
    if (!(await github.releaseExists(tag))) {
      return tag;
    }
  }
  return undefined;
}

// releaseTagsBelow is the module's release tags reachable from rev that
// name no prerelease, newest first: a prerelease run publishes its own.
async function releaseTagsBelow(rev: string, prefix: string, cwd?: string): Promise<string[]> {
  const tags = lines(await git(["tag", "--merged", rev, "--list", `${prefix}v[0-9]*`, "--sort=-v:refname"], cwd));
  return tags.filter((t) => isStableReleaseTag(t, prefix));
}

// previousTag is the module's newest release tag below tag, the start of its
// notes.
export async function previousTag(tag: string, prefix: string, cwd?: string): Promise<string | undefined> {
  return (await releaseTagsBelow(tag, prefix, cwd)).find((t) => t !== tag);
}

// unpublishedTags is the module's recent release tags below HEAD with no
// GitHub release, newest first.
export async function unpublishedTags(github: GitHub, prefix: string, cwd?: string): Promise<string[]> {
  const missing: string[] = [];
  for (const tag of (await releaseTagsBelow("HEAD", prefix, cwd)).slice(0, RECENT_RELEASE_TAGS)) {
    if (!(await github.releaseExists(tag))) {
      missing.push(tag);
    }
  }
  return missing;
}

// notesFrom is the notes the release generated from the commits, or none.
export function notesFrom(file: string | undefined): string {
  if (file === undefined) {
    return "";
  }
  try {
    return readFileSync(file, "utf8");
  } catch {
    return "";
  }
}

// publish publishes tag: notes first, then GitHub's list of the pull
// requests merged since the module's previous tag.
export async function publish(github: GitHub, tag: string, prefix: string, notes: string, latest: boolean, cwd?: string, prerelease = false): Promise<void> {
  await github.publishRelease({ tag, notes, previousTag: await previousTag(tag, prefix, cwd), latest, prerelease });
  info(`published ${tag}`);
}

// publishLeftBehind publishes every recent tag of the module an earlier run
// pushed and never published: older than the release it was left behind
// by, so never the Latest.
export async function publishLeftBehind(github: GitHub, prefix: string, cwd?: string): Promise<void> {
  for (const tag of await unpublishedTags(github, prefix, cwd)) {
    await publish(github, tag, prefix, "", false, cwd);
    info(`published ${tag}, which an earlier run left without a release`);
  }
}

// GO_PROXY is where `go get` resolves a module version.
export const GO_PROXY = "https://proxy.golang.org";
// PROXY_ATTEMPTS and PROXY_PAUSE_MS: the proxy fetches from GitHub on the
// first request, so it is given a few tries.
const PROXY_ATTEMPTS = 5;
const PROXY_PAUSE_MS = 10_000;
// RELEASE_TAG is an optional module directory — lower-case path segments —
// then the version, a release candidate's included.
const RELEASE_TAG = /^((?:[a-z0-9][a-z0-9._-]*\/)*)(v[0-9]+\.[0-9]+\.[0-9]+(?:-rc\.[0-9]+)?)$/;

// proxyInfoURL is the proxy's .info of tag's version, under the module path
// of the repository and the tag's directory.
export function proxyInfoURL(tag: string, repository: string, proxy: string = GO_PROXY): string {
  const match = RELEASE_TAG.exec(tag);
  if (match === null) {
    throw new Error(`${tag} is not a release tag ([<dir>/]vX.Y.Z[-rc.N])`);
  }
  const dir = (match[1] ?? "").replace(/\/$/, "");
  const module = `github.com/${repository}${dir === "" ? "" : `/${dir}`}`;
  // The protocol escapes upper-case letters in a module path; this
  // repository's path is lower-case, which this keeps true.
  if (/[A-Z]/.test(module)) {
    throw new Error(`${module} has upper-case letters, which the proxy protocol escapes`);
  }
  return `${proxy}/${module}/@v/${match[2]}.info`;
}

// Fetch is the part of fetch the warm-up uses.
export type Fetch = (url: string) => Promise<{ ok: boolean; status: number }>;

// warmGoProxy tells the Go module proxy about tag, so `go get …@latest` sees
// it now rather than whenever the proxy next refreshes. The release stands
// without it: a proxy that does not answer is a warning.
export async function warmGoProxy(tag: string, repository: string, fetchInfo: Fetch, pauseMs: number = PROXY_PAUSE_MS, proxy: string = GO_PROXY): Promise<boolean> {
  const url = proxyInfoURL(tag, repository, proxy);
  for (let attempt = 1; attempt <= PROXY_ATTEMPTS; attempt++) {
    try {
      const response = await fetchInfo(url);
      if (response.ok) {
        info(`the Go proxy has ${tag}`);
        return true;
      }
    } catch {
      // Tried again below.
    }
    if (attempt < PROXY_ATTEMPTS) {
      await new Promise((resolve) => setTimeout(resolve, pauseMs));
    }
  }
  warning(`the Go proxy did not take ${tag} yet; it will on its own`);
  return false;
}
