// Tag what a release pull request planned (docs/adr/0016-one-release-commit.md).
//
// A module's changelog is the manifest of its release: an entry first in it
// that no tag names is a release planned by release-plan.ts and merged. Each
// is tagged, with the deploy key that alone may create a release tag, on the
// commit that brought the entry to main — the release pull request's merge,
// whatever merged after it — and published as a GitHub release with the
// entry as its notes, then the Go proxy is told of it. The root module's
// release is GitHub's Latest; a nested module's never is. An entry no newer
// than the module's last release is refused, so a changelog edited by hand
// cannot tag a version below one released. Last, the releases an earlier
// run tagged and never published are published. Nothing here calls it yet.

import { changelogPath, entryVersion, newestEntry } from "./changelog.ts";
import { info } from "./lib/actions.ts";
import { git } from "./lib/git.ts";
import { type GitHub } from "./lib/github.ts";
import { compareVersions } from "./lib/go-modules.ts";
import { ROOT_MODULE, releasedModules, selectModule, type Module } from "./lib/modules.ts";
import { lastStableTag } from "./lib/release-policy.ts";
import { publish, publishLeftBehind, warmGoProxy, type Fetch } from "./lib/releases.ts";

// Tagger is what tagging needs, its effects injected so a test can stand in
// for GitHub, the push and the Go proxy.
export interface Tagger {
  github: GitHub;
  repository: string;
  root: string;
  fetchInfo: Fetch;
  // pauseMs is the wait between two asks of the Go proxy.
  pauseMs?: number;
  // pushTag pushes a tag made in the checkout.
  pushTag(tag: string): Promise<void>;
}

// Planned is one release a merged changelog entry asks for.
interface Planned {
  module: Module;
  tag: string;
  entry: string;
  // commit is the commit on main's first-parent line that brought it.
  commit: string;
}

// plannedReleases is the release each module's changelog at HEAD asks for
// and no tag names yet, in the module table's order.
async function plannedReleases(root: string): Promise<Planned[]> {
  const planned: Planned[] = [];
  for (const name of releasedModules(root)) {
    const module = selectModule(root, name);
    const entry = newestEntry(root, module);
    const version = entry === undefined ? undefined : entryVersion(entry);
    if (entry === undefined || version === undefined) {
      continue;
    }
    const tag = `${module.prefix}v${version}`;
    if ((await git(["tag", "--list", tag], root)).trim() !== "") {
      continue;
    }
    const last = await lastStableTag(module, "HEAD", root);
    if (last !== undefined && compareVersions(`v${version}`, last.slice(module.prefix.length)) <= 0) {
      throw new Error(`${changelogPath(module)} opens with ${version}, which no tag names: ${version} is not past ${last}`);
    }
    planned.push({ module, tag, entry, commit: await bringing(root, module) });
  }
  return planned;
}

// bringing is the newest commit on HEAD's first-parent line that changed
// module's changelog: a release pull request's merge.
async function bringing(root: string, module: Module): Promise<string> {
  return (await git(["log", "--first-parent", "-1", "--format=%H", "HEAD", "--", changelogPath(module)], root)).trim();
}

// tagReleases tags and publishes each release the changelogs ask for, then
// what earlier runs left unpublished, and returns the tags it cut.
export async function tagReleases(t: Tagger): Promise<string[]> {
  const planned = await plannedReleases(t.root);
  for (const p of planned) {
    await git(["tag", p.tag, p.commit], t.root);
    await t.pushTag(p.tag);
    await publish(t.github, p.tag, p.module.prefix, p.entry, p.module.name === ROOT_MODULE, t.root);
    await warmGoProxy(p.tag, t.repository, t.fetchInfo, t.pauseMs);
    info(`${p.module.name}: ${p.tag} tagged on ${p.commit}`);
  }
  for (const name of releasedModules(t.root)) {
    await publishLeftBehind(t.github, selectModule(t.root, name).prefix, t.root);
  }
  return planned.map((p) => p.tag);
}
