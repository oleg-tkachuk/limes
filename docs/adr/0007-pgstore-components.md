# ADR-0007: Split the meter by what a consumer uses, and pgstore along it

**Status:** Accepted
**Date:** 2026-10-29
**Deciders:** Oleg Tkachuk
**Relates to:** [ADR-0001](0001-architecture-before-1-0.md)

## Context

The core already splits the store's interfaces: the verifier takes a
`RevocationLookup`, the issuer an `IssuanceStore`, and the meter's reads,
tenant budgets and housekeeping are interfaces of their own. Two parts are
not split.

- **`meter.Meter` is one interface.** It holds `Bump` beside `Charge`,
  `Reserve`, `Settle`, `Release` and `Refund`, and `metertest.Run` checks
  all of them against a `meter.UsageStore`. A consumer that counts requests
  and spends no money can neither type its store as less than that nor have
  the suite check what it uses.
- **pgstore is one package.** It holds the records, the meter, the reports,
  the erase and the migrations together.
  - Every binary that builds a store links tern and the embedded SQL, which
    only a migration job runs.
  - `pgstore/go.mod` requires testcontainers, and through it moby and
    docker, for its tests alone. Every consumer resolves that requirement in
    its module graph.
  - The meter reads `limes.capabilities` directly: its lineage walk, the
    record's expiry, and the ceilings in the record's JSON, which it holds
    `FOR KEY SHARE` against Purge (`pgstore/meter.go`). One component depends
    on another's private columns.
  - `Store.EraseTenant` removes records and usage in one transaction.

Counting cannot do without the records. `Bump` counts on the capability and
on every ancestor, and refuses at any ceiling in the chain; a `MeterSubject`
carries only the capability's own ceilings. A request counter therefore
needs the lineage and the ancestors' ceilings from the database, exactly as
`Charge` does.

limes is in release candidates and pgstore is pre-1.0. Only the meter
interfaces in the core are frozen by limes 1.0; pgstore's layout can change
at any minor.

## Decision

1. **The meter is split in the core.**
   - `meter.RequestCounter` holds `Bump`. `meter.BudgetMeter` holds
     `Charge`, `Reserve`, `Settle`, `Release` and `Refund`. `meter.Meter`
     composes both, and `meter.UsageStore` composes it with the readers as
     it does today, so every implementation and caller of the full store is
     unchanged.
   - `metertest.RunCounter` checks a `RequestCounter` through `Bump` alone,
     with a `CounterEnv` that records capabilities but reads nothing back.
     `Run` runs it too, and its `Env` keeps its shape.
   - The budget methods get no suite of their own: a charge is checked by
     reading what it counted, so a budget meter is checked with its readers,
     as `Run` does. The ledger's checks already run only when
     `Env.Ledger` is set.
   - `storetest` stays one suite: `limes.Store` holds the records alone, and
     its census and listing checks already run only when the `Env` sets
     them.
   - This is the one part limes 1.0 waits on.
2. **A budget that nothing meters is not refused.** A store typed as a
   `RequestCounter` has no `Charge` to call, so no spend passes through limes
   unchecked. A consumer that spends outside limes while its capabilities
   carry a `Budget` caveat has not had limes enforce that budget; the
   `RequestCounter` doc says so. The issuer does not refuse such a
   capability, because the issuer is not configured with a meter and cannot
   tell.
3. **pgstore stays one package, beside `pgstore/migrate`.**
   - `pgstore` keeps the records, the meter, the ledger, the census, the
     erase, `CheckSchema` and `ReadSchemaVersion`. `CheckSchema` reads
     `limes.schema_version` and needs no tern; the version it expects is a
     constant in `pgstore/internal/pgschema`, with the other names both
     packages spell, so neither imports the other.
   - `pgstore/migrate` holds `Run` (what was `pgstore.Migrate`), tern and
     the embedded SQL, and only a migration job imports it. A test there
     pins the number of its steps to the constant `CheckSchema` expects.
     The root keeps no wrapper of `Migrate`: one would link tern into every
     binary again, which is what the move removes.
   - The meter's own packages, `pgstore/usage` and `pgstore/erase`, were
     planned and dropped once measured (Option D): Go's linker already
     leaves out every function a binary does not reach, so a binary built
     with `pgstore.NewStore` alone holds none of the meter's code.
4. **One migration chain, and its SQL is readable without tern.**
   - The chain 001–021 and `limes.schema_version` stay as they are, so an
     existing database upgrades with the next migration as before.
   - `migrate.Steps()` returns each migration as tern renders it, and says
     which ones must run outside a transaction (`CREATE INDEX
     CONCURRENTLY`). A step's `SQL()` adds what `Run` does around it: read
     committed and the scope of every tenant before it, the version recorded
     after it. `migrate.Setup()` creates the schema and the version table,
     and `migrate.Grants(roles)` the runtime roles' grants, which no
     migration holds. A consumer with a migration tool of its own applies
     those instead of running tern beside it, and `Run` can take over a
     database migrated so.
   - `Run` and `Steps` read the migrations through one loader, and a test
     holds a schema built by either, or half by each, to the same
     `pg_dump`.
5. **Usage reads the records through a view.**
   - A new migration creates `limes.capability_lineage`: `id`, `parent_id`,
     `tenant_id`, `expires_at`, and the requests, budget and copies caveats.
   - It is a plain projection of `limes.capabilities`, so that
     `FOR KEY SHARE` through it locks the record rows. The recursive walk
     stays in the meter's queries.
   - It is created `WITH (security_invoker = true)`, so the forced row
     security of `limes.capabilities` binds the caller, not the view's
     owner. This needs PostgreSQL 15 or later, which pgstore then states as
     its minimum.
   - The meter, the ledger, the charges and the erase's step that forgets
     an erased tenant read only the view. A test holds the view to each
     tenant's rows, for the runtime role and the owner, and a
     `FOR KEY SHARE` through it to the record row; another fails on any
     query of the usage files that names `limes.capabilities`. The erase's
     statements of its own records stay on the table, as the records' own.
6. **testcontainers leaves pgstore's go.mod.**
   - The tests stay in `package pgstore`: 42 of its 44 test files read its
     unexported code, so they cannot move to another module.
   - A nested module that is never released,
     `pgstore/internal/pgcontainer`, starts the container and requires
     testcontainers alone. `pgstore/internal/pgtest` runs it with `go run`
     from each test binary's `TestMain`, reads the connection string it
     writes, and asks it for a `pg_dump` by a line on its input; the
     container goes when that input closes.
   - So `go test ./...` in pgstore runs as before, and so does every task
     and workflow that runs it. The launcher lives under `pgstore/` rather
     than `tools/` so that a change to it runs pgstore's CI job.
   - A test fails when pgstore's module graph holds testcontainers, docker
     or moby again.
7. **The boundaries are checked by tools.**
   - No package of pgstore but `pgstore/migrate` depends on tern or on
     `pgstore/migrate`, as `go list` resolves their dependencies.
   - The usage files read `limes.capabilities` only through the view; a
     test greps their source for it.
   - The root module still links no database driver (`isolation_test.go`).

## Options considered

### Option A: split the meter, `pgstore/migrate`, one chain (chosen)

| Dimension | Assessment |
|---|---|
| Consumer | Types a counter as a `RequestCounter`; links no tern; resolves no testcontainers |
| Existing databases | Nothing to do: the same chain and version table |
| Cost | A core interface split, one package moved, one view, one test container module |

### Option B: a migration chain and a version table per component

Records, counter, budget and the reports would each own their tables,
migrations and version table, ordered by their dependencies, so a consumer
could create only what it uses.

**Cons:**
- Five version tables to keep for good, and an upgrade step that rewrites
  the version of every database migrated so far, proved by comparing
  schema dumps.
- The functions the row-security policies call (005) and the forced row
  security (010) belong to no component; they would need a sixth.
- The tables it saves cost a consumer nothing while they are empty.

### Option C: a package per component

`pgcore`, `records`, `watch`, `counter`, `budget`, `ledger`, `census`,
`erase` and `migrate`.

**Cons:**
- The connection, the scope helpers and the cursors would become public
  API to be shared across them.
- The linking it saves is tern and testcontainers, which Option A already
  removes; the budget code a counter links is small beside pgx.

### Option D: `pgstore/usage` and `pgstore/erase` beside the root

The meter, the ledger and the copy counters in a package of their own, and
`EraseTenant`, which spans records and usage, in another.

Measured on 2026-10-29 with `-trimpath -ldflags=-s`: a binary of pgx alone
is 7.47 MB; one that builds `pgstore.NewStore` and asks `IsRevoked`,
11.61 MB, with no symbol of the meter, the charges or the ledger in it; one
that also builds the `UsageStore` and bumps through it as a
`meter.RequestCounter`, 11.70 MB.

**Cons:**
- It shrinks no binary: the linker already leaves out what a binary does
  not reach, and a counter is the same store as a budget meter.
- It moves some 2,900 lines and 33 test files that share fixtures with the
  records, makes the connection, the scope helpers and the cursors an
  internal package of their own, and breaks `pgstore.NewUsageStore` for
  every consumer.
- The boundary it would draw is held already: the meter reads the records
  through the view, and a test fails when it does not.

### Option E: the consumer's own transaction

Every store would accept a transaction from the caller (`WithTx`), so a
consumer could commit an insert with a row of its own. The store relies on
READ COMMITTED and on `SET LOCAL` for the scope, so a foreign transaction
would have to be checked where it enters. Deferred: not needed for the
split, and not designed here.

## Consequences

- A consumer that only counts requests builds `pgstore.NewUsageStore`,
  holds it as a `meter.RequestCounter`, and runs `metertest.RunCounter`. It
  links neither tern, the embedded SQL, nor the budget code it never calls.
- A migration job imports `pgstore/migrate`. A consumer with its own
  migration tool reads `migrate.Steps` instead.
- pgstore requires PostgreSQL 15 or later.
- `docs/migration.md` notes, under the pgstore minor that moved it, the
  import that replaces `pgstore.Migrate`.
