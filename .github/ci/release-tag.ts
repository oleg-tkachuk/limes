// Tag what a release pull request planned (docs/adr/0016-one-release-commit.md).
//
// Usage: GH_TOKEN=… GITHUB_REPOSITORY=… RELEASE_DEPLOY_KEY=… RUNNER_TEMP=… node release-tag.ts
//
// A module's changelog is the manifest of its release: an entry first in it
// that no tag names is a release planned by release-plan.ts and merged. Each
// is tagged, with the deploy key that alone may create a release tag, on the
// commit that brought the entry to main — the release pull request's merge,
// whatever merged after it — and published as a GitHub release with the
// entry as its notes, then the Go proxy is told of it. The root module's
// release is GitHub's Latest; a nested module's never is. An entry no newer
// than the module's last release is refused, so a changelog edited by hand
// cannot tag a version below one released. A nested module no tag names
// yet has its release line started first (lib/seed.ts), so the plan that
// follows counts its commits from there. Last, the releases an earlier run
// tagged and never published are published.
//
// A plan merged stale is refused, before any tag is made or pushed
// (lib/stale-plan.ts): when main, as the release pull request's merge found
// it, holds a commit since the plan's base that releases a module in the
// plan, or makes one it leaves out due, the versions the plan wrote are not
// those of the code its merge holds. release-stale.ts drafts such a pull
// request while it is open; this catches one made ready by hand. The run
// fails naming the commits; the release merge is reverted, and the next
// plan counts them. A merge whose shape is not a release commit merged into
// main cannot be judged, and is tagged as before, with a warning.
//
// A release of limes while a line of candidates of its next major is open
// sets the step output follow-candidate, and release.yaml cuts the next
// candidate after it: Go orders every v1.0.0-rc.N above every 0.x, so a
// build that requires a candidate would never select the release, and a
// fix in it would reach no one on the candidates.

import { changelogPath, entryVersion, newestEntry } from "./changelog.ts";
import { info, notice, setOutput, summarize, warning } from "./lib/actions.ts";
import { withDeployKey } from "./lib/deploy-key.ts";
import { requireEnv } from "./lib/env.ts";
import { git } from "./lib/git.ts";
import { gitHubFor, type GitHub } from "./lib/github.ts";
import { runMain } from "./lib/main.ts";
import { compareVersions } from "./lib/go-modules.ts";
import { ROOT_MODULE, releaseCommits, releasedModules, selectModule, type Module, type ReleaseCommits } from "./lib/modules.ts";
import { candidateLineOpen } from "./lib/prerelease.ts";
import { lastStableTag, loadPolicy, type Policy } from "./lib/release-policy.ts";
import { publish, publishLeftBehind, warmGoProxy, type Fetch } from "./lib/releases.ts";
import { seedLine } from "./lib/seed.ts";
import { parentsOf, planBaseOf, short, staleLines, staleModules } from "./lib/stale-plan.ts";
import { tagPusher } from "./lib/tag-push.ts";

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
  // commits and policy judge whether a merged plan is stale, as the plan
  // counts.
  commits: ReleaseCommits;
  policy: Policy;
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

// MERGE_PARENTS is how many parents a release pull request's merge has:
// main before it, then the release commit.
const MERGE_PARENTS = 2;

// refuseStale fails when the merge that brought any of planned merged a
// stale plan (lib/stale-plan.ts), naming what staled it.
async function refuseStale(t: Tagger, planned: readonly Planned[]): Promise<void> {
  const problems: string[] = [];
  for (const merge of new Set(planned.map((p) => p.commit))) {
    const tags = planned.filter((p) => p.commit === merge).map((p) => p.tag);
    const parents = await parentsOf(merge, t.root);
    const [before, release] = parents;
    if (parents.length !== MERGE_PARENTS || before === undefined || release === undefined) {
      warning(`${short(merge)}, which brought ${tags.join(", ")}, is no merge of a release commit: whether its plan is stale cannot be told`);
      continue;
    }
    const based = await planBaseOf(release, before, t.root);
    if (based.base === undefined) {
      warning(`${short(merge)}, which brought ${tags.join(", ")}: ${based.unknown}; whether its plan is stale cannot be told`);
      continue;
    }
    const staled = await staleModules({ root: t.root, release, base: based.base, head: before, commits: t.commits, policy: t.policy });
    if (staled.length === 0) {
      info(`${short(merge)}: its plan, from ${short(based.base)}, is current with main as it merged, ${short(before)}`);
      continue;
    }
    problems.push(
      `${short(merge)} merged a plan of ${tags.join(", ")} made from ${short(based.base)}, but main was at ${short(before)} when it merged, past commits the plan never counted:\n${staleLines(staled).join("\n")}`,
    );
  }
  if (problems.length > 0) {
    throw new Error(
      `${problems.join("\n\n")}\n\nThose versions are stale, so nothing is tagged. Revert the release pull request's merge on main; the plan is made again after the revert's CI passes, counting these commits.`,
    );
  }
}

// tagReleases tags and publishes each release the changelogs ask for, then
// what earlier runs left unpublished, and returns the tags it cut. A stale
// plan tags nothing, not even a seeded line.
export async function tagReleases(t: Tagger): Promise<string[]> {
  await refuseStale(t, await plannedReleases(t.root));
  for (const name of releasedModules(t.root)) {
    const seeded = await seedLine(t.root, selectModule(t.root, name), t.pushTag);
    if (seeded !== undefined) {
      info(`${name}: release line seeded with ${seeded}`);
    }
  }
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

// OUTPUT_FOLLOW_CANDIDATE is the step output that has release.yaml cut a
// candidate of limes's next major after the releases tagged.
export const OUTPUT_FOLLOW_CANDIDATE = "follow-candidate";

// followWithCandidate reports whether the tags cut include a release of
// limes while a line of candidates of its next major is open.
export async function followWithCandidate(tags: readonly string[], root: string): Promise<boolean> {
  const limes = selectModule(root, ROOT_MODULE);
  if (!tags.some((t) => t.startsWith(`${limes.prefix}v`) && !t.includes("/"))) {
    return false;
  }
  return candidateLineOpen(limes, root);
}

async function main(): Promise<void> {
  const repository = requireEnv("GITHUB_REPOSITORY");
  const github = gitHubFor(requireEnv("GH_TOKEN"), repository);
  const root = (await git(["rev-parse", "--show-toplevel"])).trim();
  const tags = await withDeployKey(repository, (push) =>
    tagReleases({
      github,
      repository,
      root,
      fetchInfo: (url) => fetch(url),
      pushTag: tagPusher(push, root),
      commits: releaseCommits(),
      policy: loadPolicy(root),
    }),
  );
  if (tags.length === 0) {
    notice("no release planned is waiting for its tag");
  }
  const follow = await followWithCandidate(tags, root);
  if (follow) {
    info("limes released while its candidates of the next major are open: the next candidate follows");
  }
  setOutput(OUTPUT_FOLLOW_CANDIDATE, String(follow));
  summarize(`### Releases tagged\n\n${tags.length === 0 ? "None." : tags.map((t) => `- ${t}`).join("\n")}`);
}

if (import.meta.main) {
  await runMain(main);
}
