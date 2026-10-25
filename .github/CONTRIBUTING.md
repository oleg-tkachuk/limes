# Contributing

## Building and testing

Go (the `toolchain` in `go.mod`; the `go` directive is the oldest Go the
module supports, and CI runs the suite on it too) and [Task](https://taskfile.dev); Node 24 and
`npm ci --ignore-scripts --prefix .github/ci` for the TypeScript the workflows run, and
`npm ci --ignore-scripts --prefix .github/release` for the release-rule, release-notes, major-guard and
module-commits tests. govulncheck, actionlint and apidiff are pinned in `tools/go.mod`, yq in
`tools/yq/go.mod`, golangci-lint in `tools/golangci-lint/go.mod`, and all run through `go tool`, so they need no install.

```bash
task verify-all
```

runs gofmt, `go mod tidy` and `go mod verify`, golangci-lint, `go vet` of a
32-bit build, the unit suite, govulncheck, actionlint,
the type check and tests of the TypeScript the workflows run (`.github/ci`), the release-rule,
release-notes, major-guard and
module-commits tests, the gates of `redisreplay` (its tests on miniredis
included) and `otelmetric`, the static checks of `pgstore` and the examples that need no
database, run end to end — the same task CI runs. It
needs no database and no container; it does need the network, for the shared
Taskfile include, `go mod verify` and govulncheck's database.

`pgstore`, the reference PostgreSQL store, is a module of its own. Its tests run
the contract suites against PostgreSQL in a container, so they need a running
Docker, and run apart:

```bash
task test:pgstore
```

A pgstore migration that indexes a table an earlier migration created builds
the index without holding the table's writes: `CREATE [UNIQUE] INDEX
CONCURRENTLY`, after a `DROP INDEX CONCURRENTLY IF EXISTS` of the same index,
in a migration of nothing else marked `---- tern: disable-tx ----`, which tern
runs outside a transaction a statement at a time. A build that fails leaves an
invalid index and its version unrecorded, so the next `Migrate` drops it and
builds again. A constraint that needs an index (`PRIMARY KEY`, `UNIQUE`,
`EXCLUDE`) goes on an existing table `USING INDEX` one built so.
`TestMigrationsBuildIndexesWithoutALock` holds every migration after 013 to
this.

Every nested module — `pgstore`, `redisreplay`, `otelmetric` — builds against
the limes of the same commit, through the replace in its `go.mod`, so CI runs
its tests twice. Against that limes they are part of the required check: limes
may not break a nested module. Against the limes its `go.mod` requires, which
is what a consumer's build resolves (`task test:pinned MODULE=<module>`, the
job **Released limes (&lt;module&gt;)**), they gate that module's release
instead: a run in which they fail releases the other modules and holds that
one back. That job runs on pushes to `main` alone, whose results the release
reads; on a pull request run it by hand.

So a limes change that a nested module must follow — a value pgstore names
exhaustively, such as a `ChargeBucket`, or an API the module calls — lands
with the module in one pull request. The released-limes job of its merge fails,
as nothing released has the change yet; the release then releases limes, and
the release workflow opens one pull request raising every nested module's
requirement to that release (`build(nested): require limes vX.Y.Z in …`, merged
once green). Its merge passes the job and releases each module it raised
with every change the module held: a `build(nested)` commit — `build(limes)`
before it was renamed — is a patch of each nested module whose files it
changes, so the raised requirement is released
even when nothing else waits. A red released-limes job holds no merge: main
is judged by its required check alone. A module change
that needs a limes already released raises the requirement in its own
`go.mod`.

`redisreplay`'s contract against a real Redis runs apart too, at the address
`LIMES_REDIS_ADDR` names (`host:port`); miniredis runs another Lua, so this is
the check of the script itself:

```bash
task test:redisreplay
```

Both are jobs CI requires before a release.

## Benchmarks and soak

```bash
task bench:limes
task bench:pgstore
task bench:otelmetric
task test:pgstore:soak SOAK_DURATION=5m
```

`bench:limes`, `bench:pgstore` and `bench:otelmetric` run a module's benchmarks on this commit and
on its newest release, a round of each in turn, and compare them with
benchstat; `pgstore`'s need Docker. Each benchmark reports its allocations,
and the `Parallel` ones run a path from every processor at once. The soak
runs concurrent workers against `pgstore` — charges, refunds, holds settled,
released and abandoned, requests counted — and then checks every counter
against what they did, that no hold is left and that the pool has every
connection back. A weekly workflow, **Benchmarks and soak**, runs them all,
and can be run by hand: a slower number is a report on its summary page and
fails nothing, a soak whose counters drift fails it.

## How a change lands

- Every change goes through a pull request into `main`; CI's
  **All checks passed** is the required check.
- A pull request runs the workflows it brings, so its required check proves
  only what its own `.github/` says. A pull request from a fork is never
  merged automatically, and neither is one that changes what CI runs —
  `.github/` (workflows, the CI scripts, release tooling), `Taskfile.yaml`,
  `.taskrc*`, `.task/`, `.golangci.yml`, `tools/`, `go.work`, `vendor/`,
  `renovate.json` or `.renovaterc*`, or the root `go.mod`,
  whose `go` and `toolchain` lines pick the Go every job sets up: its green
  check is no evidence on its own, so
  a person reads those files and merges it. A nested module's `go.mod` is held
  the same way unless only its requirements change, as the release's own
  raise of a requirement of limes does: its `replace`, `ignore`, `go` and
  `toolchain` lines decide what CI tests too. Auto-merge decides this from
  `main`'s own workflow when the pull request's CI run passes, and merges
  only the commit that run tested, so a pull request cannot change the rule
  and a later push cannot ride on an earlier decision. The run must be the
  pull request's own, into `main` — not another pull request's at the same
  commit — and the merge commit carries the title that run checked. While
  `main` is red, nothing merges; once it is green again, what waited is
  merged. Every push to `main` runs every check, whatever it changed, since
  its run is what a release is vouched for by and what says `main` is green. Mark a pull request
  draft to hold it. Renovate's
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

The repository settings the workflows rely on — merge commits allowed, a
ruleset on `main` taking only a merged pull request whose **All checks
passed**, reported by GitHub Actions, passed (not up to date with `main`), a
ruleset keeping every module's release tags as they were cut, the
`automerge` environment admitting `main` alone, private vulnerability
reporting — are set by an admin, by hand:

```bash
npm ci --ignore-scripts --prefix .github/ci
GH_TOKEN=$(gh auth token) node .github/ci/repo-settings.ts
```

It is idempotent, leaves visibility and secrets alone, and lists any other
ruleset on `main` rather than change it.

A release tag can be created only with a deploy key (the
`release-tag-creation` ruleset), and the release workflow pushes its tags
with the one in the `RELEASE_DEPLOY_KEY` secret. Make it once, keep it the
repository's only deploy key with write access, and leave no copy behind:

```bash
ssh-keygen -t ed25519 -N '' -C limes-release -f release_deploy_key
gh repo deploy-key add release_deploy_key.pub --repo oleg-tkachuk/limes --title release --allow-write
gh secret set RELEASE_DEPLOY_KEY --repo oleg-tkachuk/limes < release_deploy_key
rm -f release_deploy_key release_deploy_key.pub
```

Security issues go through [SECURITY.md](SECURITY.md), not a public issue.
