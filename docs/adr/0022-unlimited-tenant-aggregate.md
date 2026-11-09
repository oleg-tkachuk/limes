# ADR-0022: A tenant without a ceiling is not one locked row

**Status:** Proposed
**Date:** 2026-11-09
**Deciders:** Oleg Tkachuk
**Relates to:** [ADR-0007](0007-pgstore-components.md), [ADR-0017](0017-budget-leases.md), [ADR-0018](0018-fewer-round-trips-per-charge.md)

## Context

Every `Charge`, `Reserve`, `Settle`, `Release`, `Refund` and lease call in
pgstore (not `Bump`, which counts requests and leaves the aggregate alone)
reads its tenant's aggregate, `limes.tenant_budgets`, `FOR UPDATE`
and writes it back before `COMMIT`: the ceiling is checked in Go between
the read and the write. After ADR-0018 step 7 a `Charge` holds that row for
two of its four round trips, and one with `onCharged` for two and the
callback (ADR-0018, Decision 5). Every metered call of one tenant queues on
it, whatever capability it comes from: the tenant's charges from every
processor, on chains that share no other row, are bounded by one row's
hold time (the figures are in ADR-0017).

A lease (ADR-0017) takes the row off the path for a caller that leases up
front. A consumer whose API clients do not lease gets the queue.

A tenant starts without a ceiling: `sqlCreateTenant` creates its aggregate
`ceiling_unlimited = true`, and it stays so until `SetTenantBudget` sets
one. Such a tenant's aggregate is checked against nothing. It is locked
only because the Go code reads, adds and writes it; a blind
`UPDATE … SET spent = spent + $1` would not help, since an updated row stays
locked to `COMMIT` too. One row is one queue, whoever writes it.

The period moves only through `SetTenantBudget` with `ResetSpend`; no
metered call rolls it over.

ADR-0018 settled how short the hold can be for a single row: locked last
(Decision 3), and not as a conditional write in SQL (its rollout, Decision
2). This ADR does not reopen that.

## Decision

1. **A tenant without a ceiling counts its spend and holds in stripes.** A
   new table `limes.tenant_budget_stripes (tenant_id, stripe, period,
   spent, reserved, updated_at)`, primary key `(tenant_id, stripe)`, holds
   signed deltas: what each stripe added to the tenant's aggregate since
   the aggregate was last folded. The aggregate's value is its row plus the
   sum of its stripes. The row is created as today, in the batch that reads
   it, before any stripe is written: no stripe exists without its row, and
   a fold always has a row to fold into. `stripe` is `pg_backend_pid() % tenantStripes`, a
   named constant (64): two calls on one connection never run at once, so a
   stripe is contended only by two connections whose pids meet modulo the
   constant.
2. **A metered call on an unlimited tenant does not lock the aggregate.**
   It already shares the tenant's advisory lock (`eraseLockClass`, taken
   first in each transaction). In the batch that would lock the aggregate
   it reads the row without a lock; when the row says
   `ceiling_unlimited`, the write batch adds the call's deltas to its
   stripe with `INSERT … ON CONFLICT (tenant_id, stripe) DO UPDATE SET
   spent = spent + EXCLUDED.spent, …`, which locks that stripe only. The
   counters, the ledger row, the hold and `onCharged` are unchanged. A
   tenant with a ceiling takes today's path.
3. **Setting a ceiling, or resetting the period, folds the stripes
   first.** `SetTenantBudget` takes the tenant's advisory lock alone, as an
   erase does: every metered call of the tenant in flight ends first, and
   none starts until it commits. Under it, it locks the aggregate, adds
   the sum of the stripes into it, deletes them, and only then applies the
   request — the ceiling checked against the folded spend, a reset closing
   the folded period. So a call that read `ceiling_unlimited` has
   committed its stripe before the fold, and no call can read the
   aggregate as unlimited after the ceiling is set. Any call that locks
   the aggregate `FOR UPDATE` and finds stripes folds them in the same
   way, so a stripe written while an old pgstore set a ceiling during a
   rolling upgrade is not lost. The fold runs before `SetTenantBudget`'s
   `ExpectedVersion` check: a mismatch rolls the fold back with the
   transaction and the stripes survive. The fold does not bump
   `resource_version`; it changes no ceiling.
4. **Signed stripes, exact sums.** A `Settle` or `Refund` may subtract on
   another stripe than the `Reserve` or `Charge` added on. A stripe may go
   negative; the sum may not, and is clamped at zero on read as the row's
   values are today (`max0`). The stripe table has no `CHECK (spent >= 0)`.
   Since every change of period goes through `SetTenantBudget`, which folds
   first, stripes only ever hold the current period's deltas: `period` on a
   stripe is a guard the fold asserts against the row's. A refund checks
   the row's period, as today, and writes its negative delta or nothing.
5. **Readers sum, in one statement.** `GetTenantBudget`, `Usage`'s tenant
   view and the ceiling check of a limited tenant read the row and the sum
   of its stripes in one statement, so both are of one snapshot;
   `UpdatedAt` is the latest of the row's and the stripes'.
   `EraseTenantUsage` deletes the stripes with the row, and they are in
   `tenant_tables.go`'s registry with `EraseRemoves`. The table has the
   same row security as `tenant_budgets`.
6. **Waiting on the tenant is measured.** pgstore times what a call waits
   for the tenant-level row it locks — the aggregate for a limited tenant,
   its stripe for an unlimited one — from the database's
   `clock_timestamp()` taken on each side of the lock, in the batch that
   takes it, so no round trip is added. It reports the wait the way
   ADR-0021 has components report: `meter.StoreEvents`, a struct of
   optional callbacks in the root `meter` package, with `OnTenantWait(ctx,
   meter.TenantWaitEvent)` carrying the tenant, the call and `Wait`; the
   struct and the event may gain fields in 1.x. pgstore takes it as
   `Config.Events`; otelmetric provides one that records a histogram. The
   types live in `meter` so that otelmetric imports nothing of pgstore and
   pgstore nothing of otelmetric. A wait longer than a named threshold is
   logged at WARN through `Config.Logger`.
7. **The benchmarks hold it**: `ChargeParallel/ceiling=none` and
   `ChargeParallel/ceiling=set` at each added latency, before and after.

## A store-managed lease for tenants with a ceiling

Considered, not the default. An implicit lease — a slice of the tenant's
remaining budget per connection or per process, sized from the recent
rate, returned on idle, expiry or a change of ceiling — keeps the ceiling
hard: no slice is granted past it. What it costs is the budget it holds
back. A tenant with ceiling `C`, `N` connections and slices of `s` sees
`ErrTenantBudgetExceeded` on one connection while up to `(N − 1)·s` is
held unused by the others: the ceiling turns soft downwards, a refusal
that a call against the row would not get, and most so near the ceiling,
where the refusal matters. `GetTenantBudget` would have to report the held
slices as reserved, or as a field of its own frozen with v1. Sizing slices
from the rate is a policy limes would own and tune for every consumer.

An explicit lease already is this, with the size chosen by the one caller
that knows its rate (ADR-0017). A consumer whose callers do not lease can
lease on their behalf, per capability, in the layer that calls `Charge`.

## Options considered

### Option A: stripes for unlimited tenants (chosen)

**Pros:** the queue goes for every tenant that has not set a ceiling, which
is every tenant until it does; the contract of every call is unchanged.
**Cons:** a second table and a fold; readers sum up to 64 rows; a tenant
that sets a ceiling is back on one row.

### Option B: append and fold

Each call inserts a delta row and housekeeping folds them. **Pros:** no
contention at all. **Cons:** a row per call until a housekeeping call
folds, which a consumer must schedule; readers sum an unbounded set
between folds. The ledger is already a row per charge; this would be a
second.

### Option C: stripes for every tenant, the ceiling checked against the sum

**Cons:** the check needs every stripe locked, which is one queue again,
or a stripe-local share of the ceiling, which is Option D.

### Option D: an implicit lease (above)

## Consequences

- **A charge on an unlimited tenant holds no row other tenants' calls or
  its own tenant's other connections wait on**, except its chain's
  counters and its own stripe. Two connections whose pids meet modulo the
  stripe count share a stripe and queue on it as all of them queue on the
  row today.
- **`Charge`'s contract does not change**: exact, atomic, its ledger row
  and `onCharged` on its transaction. No `!`; the schema change is
  pgstore's, with a migration.
- **`SetTenantBudget` waits for the tenant's calls in flight** and holds
  new ones while it folds, as an erase does.
- **`meter.StoreEvents` and pgstore's `Config.Events` are new API**,
  frozen with v1, and grow as ADR-0021's hooks do.
- **Rolling upgrade**: an old pgstore reads the row without the stripes, so
  its `GetTenantBudget` under-reports what new peers striped until the
  next fold; it sets a ceiling without folding. The migration guide says to
  set no ceiling until every peer runs the new version.
- **Nothing here blocks 1.0**: no contract changes, and the API added is
  struct of optional callbacks.

## Rollout

1. The benchmark rows and ADR-0017's figures after ADR-0018 step 7.
2. This ADR.
3. `meter.StoreEvents`, pgstore's `Config.Events`, otelmetric's recorder,
   the WARN line.
4. The stripes: migration, the unlimited path, the fold, the readers,
   erasure; `metertest` for every store, and cases for a ceiling set while
   calls are in flight and for signed stripes.
5. The benchmarks after, recorded here.
