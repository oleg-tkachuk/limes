// The pinned semantic-release, run for one module.

import { join } from "node:path";

import { withoutWorkflowCommands } from "./actions.ts";
import { ROOT_MODULE, type Module } from "./modules.ts";
import { runInherited } from "./spawn.ts";

// PUSH_URL_ENV hands semantic-release the URL it pushes a tag to:
// modules.cjs reads it as repositoryUrl, and release.test.ts holds the two
// to one name.
export const PUSH_URL_ENV = "RELEASE_PUSH_URL";
// RELEASE_DIR is where semantic-release runs, beside its config and lockfile.
const RELEASE_DIR = ".github/release";
// MODULE_ENV picks the module semantic-release releases (modules.cjs).
const MODULE_ENV = "RELEASE_MODULE";
// NOTES_ENV names the file release-notes-file.cjs writes the notes to.
const NOTES_ENV = "RELEASE_NOTES_FILE";

// SEMANTIC_RELEASE_BIN is the pinned semantic-release's executable, as npm
// ci installs it beside the release tooling's lockfile.
const SEMANTIC_RELEASE_BIN = ["node_modules", ".bin", "semantic-release"];
// SEMANTIC_RELEASE_ARGS run it outside the CI it would otherwise detect, and
// DRY_RUN_ARG without tagging: a candidate's notes.
const SEMANTIC_RELEASE_ARGS = ["--no-ci"];
export const DRY_RUN_ARG = "--dry-run";

// semanticReleaseCommand is the command that runs the pinned semantic-release
// in root's checkout: its installed executable, never npx, which would fetch
// one from the registry were it missing.
export function semanticReleaseCommand(root: string): [string, string[]] {
  return [join(root, RELEASE_DIR, ...SEMANTIC_RELEASE_BIN), [...SEMANTIC_RELEASE_ARGS]];
}

// semanticRelease runs the pinned semantic-release for module, with the
// runner reading no workflow command from its log: it logs every commit
// message it reads, and this job holds a write token.
export function semanticRelease(root: string, token: string, extra: readonly string[] = []): (module: Module, notesFile: string) => Promise<void> {
  const [file, args] = semanticReleaseCommand(root);
  return (module, notesFile) =>
    withoutWorkflowCommands(() =>
      runInherited(file, [...args, ...extra], {
        cwd: join(root, RELEASE_DIR),
        env: {
          ...process.env,
          GITHUB_TOKEN: token,
          [NOTES_ENV]: notesFile,
          ...(module.name === ROOT_MODULE ? {} : { [MODULE_ENV]: module.name }),
        },
      }),
    );
}
