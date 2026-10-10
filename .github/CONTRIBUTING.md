# Contributing

## Building and testing

Go (the `toolchain` in `go.mod`; the `go` directive is the oldest Go the
module supports, and CI runs the suite on it too), [Task](https://taskfile.dev) and
[golangci-lint](https://golangci-lint.run); Node and
`npm ci --ignore-scripts --prefix .github/release` for the release-rule test.
govulncheck and actionlint are pinned in `tools/go.mod`, yq in
`tools/yq/go.mod`, and all run through `go tool`, so they need no install.

```bash
task verify-all
```

runs gofmt, golangci-lint, the unit suite, govulncheck, actionlint and the tests
of the workflow scripts — the same task CI runs. The suite needs no database, no
network and no container.

## How a change lands

- Every change goes through a pull request into `main`; CI's
  **All checks passed** is the required check.
- Commit subjects follow [Conventional Commits](https://www.conventionalcommits.org):
  the release is computed from them (`feat` a minor, `fix` a patch, pre-1.0 a
  breaking change a minor), so a non-conforming subject releases nothing.
- New behaviour comes with a test; a bug fix with a test that reproduced it.
- A change to the token wire format regenerates the golden fixtures in the
  same commit — see `goldengen_test.go` and `biscuit_crosslang_test.go`.

Security issues go through [SECURITY.md](SECURITY.md), not a public issue.
