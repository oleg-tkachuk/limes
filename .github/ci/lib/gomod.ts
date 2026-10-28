// What of a go.mod decides more than which versions a module requires.
//
// A go.mod is read line by line, as the go command reads it: a directive is a
// keyword and its arguments, or a block of them between `keyword (` and `)`;
// `//` starts a comment, but not inside a quoted argument (a Go string,
// interpreted or raw), whose text it is part of. Every directive but require — go, toolchain, replace,
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
// QUOTE and RAW_QUOTE delimit a quoted argument, as Go delimits an
// interpreted and a raw string; ESCAPE escapes the next character of an
// interpreted one.
const QUOTE = '"';
const RAW_QUOTE = "`";
const ESCAPE = "\\";

// commentStart is where line's comment starts, or -1 when it has none: the
// first COMMENT outside a quoted argument.
export function commentStart(line: string): number {
  let quote: string | undefined;
  for (let i = 0; i < line.length; i++) {
    const c = line[i];
    if (quote !== undefined) {
      if (quote === QUOTE && c === ESCAPE) {
        i++;
      } else if (c === quote) {
        quote = undefined;
      }
      continue;
    }
    if (c === QUOTE || c === RAW_QUOTE) {
      quote = c;
    } else if (line.startsWith(COMMENT, i)) {
      return i;
    }
  }
  return -1;
}

// statementsBeyondRequirements is every statement of the go.mod text but its
// requirements, normalized.
export function statementsBeyondRequirements(text: string): string[] {
  const statements: string[] = [];
  let block: string | undefined;
  for (const raw of text.split("\n")) {
    const line = statementOf(raw);
    if (line === "") {
      continue;
    }
    const read = block === undefined ? readStatement(line) : readInBlock(block, line);
    block = read.block;
    statements.push(...read.statements);
  }
  return statements;
}

// statementOf is raw without its comment, its space collapsed.
function statementOf(raw: string): string {
  const at = commentStart(raw);
  return (at < 0 ? raw : raw.slice(0, at)).trim().split(/\s+/).join(" ");
}

// readStatement reads a line outside a block: one opening a block, or a
// statement in its own right, a requirement being none beyond it.
function readStatement(line: string): { block: string | undefined; statements: string[] } {
  const [keyword = "", ...rest] = line.split(" ");
  if (rest.length === 1 && rest[0] === BLOCK_OPEN) {
    return { block: keyword, statements: [] };
  }
  return { block: undefined, statements: keyword === REQUIRE ? [] : [line] };
}

// readInBlock reads a line of block: its close, or its statement, a
// requirement being none beyond it.
function readInBlock(block: string, line: string): { block: string | undefined; statements: string[] } {
  if (line === BLOCK_CLOSE) {
    return { block: undefined, statements: [] };
  }
  return { block, statements: block === REQUIRE ? [] : [`${block} ${line}`] };
}

// changesBeyondRequirements reports whether two go.mod texts differ in
// anything but their requirements.
export function changesBeyondRequirements(before: string, after: string): boolean {
  const a = statementsBeyondRequirements(before);
  const b = statementsBeyondRequirements(after);
  return a.length !== b.length || a.some((s, i) => s !== b[i]);
}
