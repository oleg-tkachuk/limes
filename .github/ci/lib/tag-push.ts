// Pushing a release tag, as a step a run may find already done.

import { warning } from "./actions.ts";
import { git, lines } from "./git.ts";

// Git runs git with args in cwd and returns its standard output.
export type Git = (args: readonly string[], cwd?: string) => Promise<string>;

// PEELED marks the ls-remote line of the commit an annotated tag points at.
const PEELED = "^{}";

// tagPusher pushes a tag made in root to push. A push that fails while the
// remote already holds the tag at the same commit is taken as done: GitHub
// has answered a tag push it applied with "reference already exists"
// (v1.0.0-rc.63), and a retry of a run finds its own tag there too. A tag
// at another commit, or none, fails as the push did.
export function tagPusher(push: string, root: string, run: Git = git): (tag: string) => Promise<void> {
  return async (tag) => {
    try {
      await run(["push", "--quiet", push, `refs/tags/${tag}`], root);
      return;
    } catch (err) {
      const local = (await run(["rev-parse", `refs/tags/${tag}^{commit}`], root)).trim();
      let remote: string | undefined;
      try {
        remote = await remoteTagCommit(push, tag, root, run);
      } catch (lookup) {
        throw new Error(`${pushFailure(err)}; reading the remote's ${tag} failed too: ${messageOf(lookup)}`, { cause: err });
      }
      if (remote !== local) {
        throw new Error(`${pushFailure(err)}; the remote's ${tag} is ${remote ?? "absent"}, not ${local}`, { cause: err });
      }
      warning(`${tag}: the push failed, but the remote already holds it at ${local}, so it is taken as pushed: ${messageOf(err)}`);
    }
  };
}

// remoteTagCommit is the commit the remote's tag points at, peeled from an
// annotated tag, or undefined when the remote has no such tag.
export async function remoteTagCommit(remote: string, tag: string, cwd?: string, run: Git = git): Promise<string | undefined> {
  const ref = `refs/tags/${tag}`;
  const refs = new Map(
    lines(await run(["ls-remote", remote, ref, `${ref}${PEELED}`], cwd)).map((line) => {
      const [sha, name] = line.split("\t");
      return [name, sha] as const;
    }),
  );
  return refs.get(`${ref}${PEELED}`) ?? refs.get(ref);
}

function pushFailure(err: unknown): string {
  return `pushing the tag failed: ${messageOf(err)}`;
}

function messageOf(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
