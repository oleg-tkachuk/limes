# Contributing

## Building and testing

Go (the version in `go.mod`), [Task](https://taskfile.dev) and
[golangci-lint](https://golangci-lint.run); Node and
`npm ci --prefix .github/release` for the release-rule test; `yq` for the
CI-relevance test. govulncheck and actionlint are pinned in `tools/go.mod` and
run through `go tool`, so they need no install.

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
