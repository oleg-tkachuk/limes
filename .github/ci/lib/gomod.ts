// What of a go.mod decides more than which versions a module requires.
//
// A go.mod is read line by line, as the go command reads it: a directive is a
// keyword and its arguments, or a block of them between `keyword (` and `)`;
// `//` starts a comment. Every directive but require — go, toolchain, replace,
// exclude, retract, ignore, tool, godebug, module — is kept, in order, as
// keyword and arguments with their spacing collapsed, so two files that
// differ only in their requirements, their comments and their layout keep the
// same statements.
//
// golang.org/x/mod/modfile is the go.mod parser, and it is Go: the merge job
// that reads this sets up Node alone, so the grammar it needs — directives,
// blocks, comments — is read here and pinned by gomod.test.ts.

// REQUIRE is the directive a version bump changes.
const REQUIRE = "require";
// BLOCK_OPEN ends the line that opens a block; BLOCK_CLOSE is the line that
// closes one.
const BLOCK_OPEN = "(";
const BLOCK_CLOSE = ")";
// COMMENT starts a comment.
const COMMENT = "//";

// statementsBeyondRequirements is every statement of the go.mod text but its
// requirements, normalized.
export function statementsBeyondRequirements(text: string): string[] {
  const statements: string[] = [];
  let block: string | undefined;
  for (const raw of text.split("\n")) {
    const at = raw.indexOf(COMMENT);
    const line = (at < 0 ? raw : raw.slice(0, at)).trim().split(/\s+/).join(" ");
    if (line === "") {
      continue;
    }
    if (block !== undefined) {
      if (line === BLOCK_CLOSE) {
        block = undefined;
      } else if (block !== REQUIRE) {
        statements.push(`${block} ${line}`);
      }
      continue;
    }
    const [keyword = "", ...rest] = line.split(" ");
    if (rest.length === 1 && rest[0] === BLOCK_OPEN) {
      block = keyword;
      continue;
    }
    if (keyword !== REQUIRE) {
      statements.push(line);
    }
  }
  return statements;
}

// changesBeyondRequirements reports whether two go.mod texts differ in
// anything but their requirements.
export function changesBeyondRequirements(before: string, after: string): boolean {
  const a = statementsBeyondRequirements(before);
  const b = statementsBeyondRequirements(after);
  return a.length !== b.length || a.some((s, i) => s !== b[i]);
}
