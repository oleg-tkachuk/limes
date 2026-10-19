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
