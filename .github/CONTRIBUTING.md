# Contributing

## Building and testing

Go (the `toolchain` in `go.mod`; the `go` directive is the oldest Go the
module supports, and CI runs the suite on it too) and [Task](https://taskfile.dev); Node and
`npm ci --ignore-scripts --prefix .github/release` for the release-rule, release-notes, major-guard and
module-commits tests. govulncheck, actionlint and apidiff are pinned in `tools/go.mod`, yq in
`tools/yq/go.mod`, golangci-lint in `tools/golangci-lint/go.mod`, and all run through `go tool`, so they need no install.

```bash
task verify-all
```

runs gofmt, `go mod tidy` and `go mod verify`, golangci-lint, `go vet` of a
32-bit build, the unit suite, govulncheck, actionlint,
the tests of the workflow scripts, the release-rule, release-notes, major-guard and
module-commits tests, the gate of `redisreplay` (its tests on miniredis
included), the static checks of `pgstore` and the examples that need no
database, run end to end — the same task CI runs. It
needs no database and no container; it does need the network, for the shared
Taskfile include, `go mod verify` and govulncheck's database.

`pgstore`, the reference PostgreSQL store, is a module of its own. Its tests run
the contract suites against PostgreSQL in a container, so they need a running
Docker, and run apart:

```bash
task test:pgstore
```

CI runs them twice. Against the limes of the same commit, through the replace
in `pgstore/go.mod`, they are part of the required check: limes may not break
pgstore. Against the limes that `go.mod` requires, which is what a consumer's
build resolves (`task test:pgstore:pinned`, the job **PostgreSQL store,
released limes**), they gate pgstore's release instead: a run in which they
fail releases limes and redisreplay, and holds pgstore back.

So a limes change that pgstore must follow — a value pgstore names
exhaustively, such as a `ChargeBucket`, or an API pgstore calls — lands with
pgstore in one pull request. Its released-limes job fails, as nothing released
has the change yet; the merge releases limes, and the release workflow opens
a pull request raising each nested module's requirement to that release
(`build(pgstore): require limes vX.Y.Z`, queued to merge once green). That
pull request passes the job, and its merge releases pgstore with every change
it held: a nested module's own `build` commit is a patch of it, so the raised
requirement is released even when nothing else waits. A pgstore change that needs
a limes already released raises the requirement in its own `go.mod`.

`redisreplay`'s contract against a real Redis runs apart too, at the address
`LIMES_REDIS_ADDR` names (`host:port`); miniredis runs another Lua, so this is
the check of the script itself:

```bash
task test:redisreplay
```

Both are jobs CI requires before a release.

## How a change lands

- Every change goes through a pull request into `main`; CI's
  **All checks passed** is the required check.
- A pull request runs the workflows it brings, so its required check proves
  only what its own `.github/` says. A pull request from a fork is never
  merged automatically, and neither is one that changes what CI runs —
  `.github/` (workflows, scripts, release tooling), `Taskfile.yaml`,
  `.golangci.yml`, `tools/` or the root `go.mod`, whose `go` and `toolchain`
  lines pick the Go every job sets up: its green check is no evidence on its own, so
  a person reads those files and merges it. Auto-merge decides this from
  `main`'s own workflow, so a pull request cannot change the rule. Renovate's
  own pull requests are the exception: Renovate merges them itself once green
  (`renovate.json`), those bumping workflow actions, pinned tools or the task
  library included.
- Commit subjects follow [Conventional Commits](https://www.conventionalcommits.org):
  the release is computed from them (`feat` a minor; `fix`, `perf` and
  `security` a patch; pre-1.0 a breaking change a minor), so a non-conforming subject releases nothing.
- A change that breaks the Go API is marked breaking — `type!:` or a
  `BREAKING CHANGE:` footer — in one of its commits. CI's **API compatibility**
  job compares the pull request's API with its base using apidiff and fails an
  unmarked break. Before 1.0.0 the change also adds its entry to
  [docs/migration.md](../docs/migration.md) — what each name was and what it
  is — and the **Breaking markers** job fails a marked change that leaves
  that file alone. From 1.0.0 on it fails every break, and CI's **Breaking
  markers** job fails any commit marked breaking — documentation-only ones
  included — since the release would tag a v2 the Go proxy ignores; a v2 is a
  new module path, planned on its own.
- New behaviour comes with a test; a bug fix with a test that reproduced it.
- A change to the token wire format regenerates the golden fixtures in the
  same commit — see `goldengen_test.go` and `biscuit_crosslang_test.go`.

Security issues go through [SECURITY.md](SECURITY.md), not a public issue.
