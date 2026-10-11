# Contributing

## Building and testing

Go (the `toolchain` in `go.mod`; the `go` directive is the oldest Go the
module supports, and CI runs the suite on it too), [Task](https://taskfile.dev) and
[golangci-lint](https://golangci-lint.run); Node and
`npm ci --ignore-scripts --prefix .github/release` for the release-rule and release-notes tests.
govulncheck and actionlint are pinned in `tools/go.mod`, yq in
`tools/yq/go.mod`, and all run through `go tool`, so they need no install.

```bash
task verify-all
```

runs gofmt, `go mod tidy` and `go mod verify`, golangci-lint, the unit suite,
govulncheck, actionlint, the tests of the workflow scripts, and the release-rule
and release-notes tests — the same task CI runs. The suite needs no database, no
network and no container.

## How a change lands

- Every change goes through a pull request into `main`; CI's
  **All checks passed** is the required check.
- Commit subjects follow [Conventional Commits](https://www.conventionalcommits.org):
  the release is computed from them (`feat` a minor; `fix`, `perf` and
  `security` a patch; pre-1.0 a breaking change a minor), so a non-conforming subject releases nothing.
- A change that breaks the Go API is marked breaking — `type!:` or a
  `BREAKING CHANGE:` footer — in one of its commits. CI's **API compatibility**
  job compares the pull request's API with its base using apidiff and fails an
  unmarked break.
- New behaviour comes with a test; a bug fix with a test that reproduced it.
- A change to the token wire format regenerates the golden fixtures in the
  same commit — see `goldengen_test.go` and `biscuit_crosslang_test.go`.

Security issues go through [SECURITY.md](SECURITY.md), not a public issue.
