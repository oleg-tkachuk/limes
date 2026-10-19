// Path patterns as GitHub's workflow filters spell them: `*` within one path
// segment, `**/` across any number of directories, every other character as
// itself.

// globToRegExp is the expression a pattern of that syntax matches a whole
// path with.
export function globToRegExp(pattern: string): RegExp {
  let source = "";
  for (let i = 0; i < pattern.length; i++) {
    if (pattern.startsWith("**/", i)) {
      source += "(?:.*/)?";
      i += "**/".length - 1;
    } else if (pattern[i] === "*") {
      source += "[^/]*";
    } else {
      source += (pattern[i] ?? "").replace(/[.+?^${}()|[\]\\]/g, "\\$&");
    }
  }
  return new RegExp(`^${source}$`);
}

// matchesAny reports whether path matches one of patterns.
export function matchesAny(path: string, patterns: readonly string[]): boolean {
  return patterns.some((pattern) => globToRegExp(pattern).test(path));
}

// shellPatternToRegExp is the expression a bash [[ path == pattern ]] test
// matches a whole path with: `*` across any characters, `/` included, `?`
// one character, `[…]` one of a class.
export function shellPatternToRegExp(pattern: string): RegExp {
  let source = "";
  for (let i = 0; i < pattern.length; i++) {
    const c = pattern[i] ?? "";
    if (c === "*") {
      source += ".*";
    } else if (c === "?") {
      source += ".";
    } else if (c === "[") {
      const end = pattern.indexOf("]", i + 1);
      if (end < 0) {
        throw new Error(`pattern ${pattern}: an unclosed [`);
      }
      source += `[${pattern.slice(i + 1, end).replace(/[\\\]^]/g, "\\$&")}]`;
      i = end;
    } else {
      source += c.replace(/[.+?^${}()|[\]\\]/g, "\\$&");
    }
  }
  return new RegExp(`^${source}$`);
}
