// Pushing a branch as a token's owner, so the push starts CI as a push
// with the workflow's own token would not.

import { execute } from "./exec.ts";

// GITHUB_HOST is where the push authenticates.
const GITHUB_HOST = "https://github.com/";

// pushAs pushes with token, given to git through the environment of the
// push alone: no credential is written to a config file, and none reaches
// an argument list.
export function pushAs(token: string, repository: string, root: string): (branch: string) => Promise<void> {
  const header = `AUTHORIZATION: basic ${Buffer.from(`x-access-token:${token}`).toString("base64")}`;
  return async (branch) => {
    await execute("git", ["push", "--quiet", "--force", `${GITHUB_HOST}${repository}.git`, `HEAD:refs/heads/${branch}`], {
      cwd: root,
      env: { ...process.env, GIT_CONFIG_COUNT: "1", GIT_CONFIG_KEY_0: `http.${GITHUB_HOST}.extraheader`, GIT_CONFIG_VALUE_0: header },
    });
  };
}
