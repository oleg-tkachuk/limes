# Contributing

## Building and testing

You need:

- Go: the `toolchain` in `go.mod`. The `go` directive is the oldest Go the
  module supports, and CI runs the suite on it too.
- [Task](https://taskfile.dev).
- Node 24, with `npm ci --ignore-scripts --prefix .github/ci` for the
  TypeScript the workflows run, and
  `npm ci --ignore-scripts --prefix .github/release` for the release-rule,
  release-notes, major-guard and module-commits tests.

govulncheck, actionlint, apidiff and benchstat are pinned in
`tools/go.mod`, yq in `tools/yq/go.mod`, golangci-lint in
`tools/golangci-lint/go.mod`. All run through `go tool`, so none needs an
install.

```bash
task verify-all
```

runs the task library's pin and lock check, gofmt, `go mod tidy` and
`go mod verify`, golangci-lint, `go vet` of a 32-bit build, the unit suite,
govulncheck, actionlint, the type check and tests of the TypeScript the
workflows run (`.github/ci`), the release-rule, release-notes, major-guard
and module-commits tests, the gates of `redisreplay` (its tests on
miniredis included), `otelmetric`, `exchange` and `biscuit`, the static
checks of `pgstore`, and the examples that need no database, run end to
end. CI's **Verify** job runs the same, but golangci-lint, which its
**Lint** job runs. It needs no database and no container, but needs the
network for `go mod verify` and govulncheck's database.

The gates' shared tasks come from a task library pinned in
`Taskfile.yaml`. What the pin fetched is committed under `.task/remote`,
and `.taskrc.yml` has every run load it from there, so no `task` command
fetches it. Move the pin and its lock together, and only this way:

```bash
task tasklib:bump VERSION=<tag>
```

`task tasklib:check`, part of the gate, fails when the two have come
apart. A pin moved without its lock — Renovate's pull request for a new
library release is one, as Renovate cannot run a task — loads nothing
until the lock follows it. On that branch, run:

```bash
task --yes --offline=false --download tasklib:lock
```

`pgstore`, the reference PostgreSQL store, is a module of its own. Its
tests run the contract suites against PostgreSQL in a container, so they
need a running Docker, and run apart:

```bash
task test:pgstore
```

A pgstore migration that indexes a table an earlier migration created
builds the index without holding the table's writes: `CREATE [UNIQUE]
INDEX CONCURRENTLY`, after a `DROP INDEX CONCURRENTLY IF EXISTS` of the
same index, in a migration of nothing else, marked
`---- tern: disable-tx ----`, which tern runs outside a transaction one
statement at a time. A build that fails leaves an invalid index and its
version unrecorded, so the next `migrate.Run` drops it and builds it
again. A constraint that needs an index (`PRIMARY KEY`, `UNIQUE`,
`EXCLUDE`) goes on an existing table `USING INDEX` one built so.
`TestMigrationsBuildIndexesWithoutALock` holds every migration after 013
to this.

Every nested module — `pgstore`, `redisreplay`, `otelmetric`, `exchange`,
`biscuit` — builds against the limes of the same commit, through the
replace in its `go.mod`, so CI runs its tests against it as part of the
required check: limes may not break a nested module. A limes change a
nested module must follow — a value pgstore names exhaustively, such as a
`ChargeBucket`, or an API the module calls — lands with the module in one
pull request.

Every module is released from one commit
([ADR-0016](../docs/adr/0016-one-release-commit.md)). After a green push
to `main`, the release workflow proposes one release pull request
(`chore(release): …`) once a module is due under
`.github/release/release-policy.json`, or when run by hand. It is merged
once green, or by hand for a major. It raises the limes requirement of
every nested module changed since its last release to the limes it
releases, so each is released with it, and adds each module's changelog
entry. Its merge commit is tagged for every module at once, so a nested
module released requires the limes tagged beside it. One unchanged keeps
the release CI has just tested against that limes.
`task test:pinned MODULE=<module>` runs a module's tests against the limes
its `go.mod` requires, as a consumer's build resolves it.

`redisreplay`'s contract against a real Redis runs apart too, at the
address `LIMES_REDIS_ADDR` names (`host:port`). miniredis runs another
Lua, so this is the check of the script itself:

```bash
task test:redisreplay
```

CI requires both before a release.

## Benchmarks and soak

```bash
task bench:limes
task bench:pgstore
task bench:otelmetric
task test:pgstore:soak SOAK_DURATION=5m
```

`bench:limes`, `bench:pgstore` and `bench:otelmetric` run a module's
benchmarks on this commit and on its newest release, a round of each in
turn, and compare them with benchstat; `pgstore`'s need Docker. Each
benchmark reports its allocations, and the `Parallel` ones run a path from
every processor at once. The soak runs concurrent workers against
`pgstore` — charges, refunds, holds settled, released and abandoned,
requests counted — then checks every counter against what they did, that
no hold is left, and that the pool has every connection back. The weekly
**Benchmarks and soak** workflow runs them all, and can be run by hand. A
slower number is a report on its summary page and fails nothing; a soak
whose counters drift fails it.

## How a change lands

- Every change goes through a pull request into `main`. CI's **All checks
  passed** is the required check.
- A pull request runs the workflows it brings, so its required check
  proves only what its own `.github/` says. A pull request from a fork is
  never merged automatically, and neither is one that changes what CI
  runs: `.github/` (workflows, CI scripts, release tooling), any
  `Taskfile.*`, `.taskrc*`, `.task/`, `.golangci.*`, `tools/`, the root
  `go.mod` (its `go` and `toolchain` lines pick the Go every job sets up),
  `go.work*` or `vendor/` at any depth, `renovate.json*` or
  `.renovaterc*`. Its green check is no evidence on its own, so a person
  reads those files and merges it. A nested module's `go.mod` is held the
  same way unless only its requirements change, as in the release's own
  raise of the limes requirement: its `replace`, `ignore`, `go` and
  `toolchain` lines decide what CI tests too.
- Auto-merge decides this from `main`'s own workflow when the pull
  request's CI run passes, and merges only the commit that run tested, so
  a pull request cannot change the rule and a later push cannot ride on
  an earlier decision. The run must be the pull request's own, into
  `main` — not another pull request's at the same commit — and the merge
  commit carries the title that run checked. While `main` is red, nothing
  merges; once it is green again, what waited is merged. Every push to
  `main` runs every check, whatever it changed: its run is what a release
  is vouched for by, and what says `main` is green. Mark a pull request
  draft to hold it.
- Renovate's own pull requests are the exception: Renovate merges them
  itself once green (`renovate.json`), those bumping workflow actions or
  pinned tools included. The one that moves the task library's pin stays
  red until a person has refreshed its lock.
- Commit subjects follow
  [Conventional Commits](https://www.conventionalcommits.org): the release
  is computed from them (`feat` a minor; `fix`, `perf` and `security` a
  patch; before 1.0, a breaking change a minor), so a non-conforming
  subject releases nothing.
- Each module's `CHANGELOG.md` is written from its releases' notes: the
  release pull request adds each module's entry, and CI holds every entry
  to a release tag (`.github/ci/changelog.ts`). Nobody edits a changelog
  by hand.
- A change that breaks the Go API is marked breaking — `type!:` or a
  `BREAKING CHANGE:` footer — in one of its commits. CI's **API
  compatibility** job compares the pull request's API with its base using
  apidiff and fails an unmarked break. CI's **Breaking markers** job fails
  a change to the error codes or the defaults that no commit marks. Before
  1.0.0 a marked change also adds its entry to
  [docs/migration.md](../docs/migration.md) — what each name was and what
  it is — and **Breaking markers** fails one that leaves that file alone.
  From 1.0.0 on, **API compatibility** fails every break, and **Breaking
  markers** fails any commit marked breaking, documentation-only ones
  included, since the release would tag a v2 the Go proxy ignores. A v2 is
  a new module path, planned on its own.
- New behaviour comes with a test; a bug fix with a test that reproduced
  it.
- A change to the token wire format regenerates the golden fixtures in
  the same commit: see `goldengen_test.go` and
  `biscuit/biscuit_crosslang_test.go`.

## Repository settings

The settings the workflows rely on are set by an admin, by hand: merge
commits allowed; a ruleset on `main` taking only a merged pull request
whose **All checks passed**, reported by GitHub Actions, passed (not
required up to date with `main`); rulesets keeping every module's release
tags as they were cut; the `automerge` and `release` environments
admitting `main` alone; private vulnerability reporting.

```bash
npm ci --ignore-scripts --prefix .github/ci
GH_TOKEN=$(gh auth token) node .github/ci/repo-settings.ts
```

It is idempotent, leaves visibility and secrets alone, lists any other
ruleset rather than change it, and warns of `AUTOMERGE_TOKEN` or
`RELEASE_DEPLOY_KEY` set on the repository instead of its environment.

A release tag can be created only with a deploy key (the
`release-tag-creation` ruleset). The release workflow pushes its tags with
the key in the `RELEASE_DEPLOY_KEY` secret of the `release` environment.
Make it once, keep it the repository's only deploy key with write access,
and leave no copy behind:

```bash
ssh-keygen -t ed25519 -N '' -C limes-release -f release_deploy_key
gh repo deploy-key add release_deploy_key.pub --repo oleg-tkachuk/limes --title release --allow-write
gh secret set RELEASE_DEPLOY_KEY --repo oleg-tkachuk/limes --env release < release_deploy_key
rm -f release_deploy_key release_deploy_key.pub
```

Security issues go through [SECURITY.md](SECURITY.md), not a public issue.
