# ADR-0018: Fewer round trips per charge

**Status:** Proposed
**Date:** 2026-10-31
**Deciders:** Oleg Tkachuk
**Relates to:** [ADR-0007](0007-pgstore-components.md), [ADR-0017](0017-budget-leases.md)

## Context

pgstore's `Charge` sends one statement at a time and waits for each answer:
16 round trips, `Bump` 10, whatever the chain's depth
(`task bench:pgstore:scale`). Traced, one `Charge` is:

| # | Statement | Needs |
|---|---|---|
| 1 | `BEGIN` | — |
| 2 | the scope's `set_config` (`Config.Scope`) | — |
| 3 | the tenant's shared advisory lock, which erasure takes exclusively | — |
| 4 | is the tenant erased | — |
| 5 | was this charge recorded already (`ExternalRef`) | — |
| 6 | the lineage, by a recursive walk | — |
| 7 | the lineage's rows, `FOR KEY SHARE` | 6 |
| 8 | the tenant's aggregate, created if absent | — |
| 9 | the tenant's aggregate, `FOR UPDATE` | 8 |
| 10 | the chain's counters, created if absent | 6 |
| 11 | the chain's counters, `FOR UPDATE` | 10 |
| 12 | was it recorded while this one waited for the locks | 9, 11 |
| 13 | the tenant's aggregate written | the ceilings checked in Go |
| 14 | the chain's counters written | 〃 |
| 15 | the ledger row | 〃 |
| 16 | `COMMIT` | — |

Each statement waits for the one before, though most need nothing from it.
The tenant's aggregate is locked at 9 and held to 16: eight round trips, in
which every other charge of the tenant waits. With the database a
millisecond away, that is what caps a tenant's charges (ADR-0017's table:
about 48 a second, and about 12 at two milliseconds).

The order the locks are taken in — the tenant's aggregate, then the
counters by id, then the copies' (`lock`, `pgstore/meter.go`) — is the one
every call that writes counters takes, so none deadlocks another.

## Decision

1. **A charge is three batches.** pgstore sends the statements that need
   nothing from each other together, as one pgx batch, and waits once:
   - *Read:* `BEGIN`, the scope, the advisory lock, the erased check, the
     replay lookup, and the lineage with its rows locked `FOR KEY SHARE` in
     one statement (6 and 7 together).
   - *Lock:* the chain's counters created and locked, and the replay
     lookup again under the locks.
   - *Write:* the counters, the tenant's aggregate, the ledger row and
     `COMMIT`.

   The ceilings are checked in Go between the second and the third, as
   now. `BEGIN` and `COMMIT` ride in the batches on the connection itself,
   past pgx's `Tx`, which sends each alone: pgstore wraps the connection in
   a `pgx.Tx` of its own, whose `Commit` and `Rollback` send the statement,
   as `callbackTx` (`tx.go`) already implements one. Step 2 of the rollout
   proves that pgx and PostgreSQL keep the transaction across the batches
   before any other step relies on it. `Bump`, `Reserve`, `Settle`, `Release` and `Refund` take the same
   three steps.
2. **The tenant's aggregate is locked last, in the write itself.** One
   statement creates the row if absent (8), checks the ceiling and adds the
   spend: an `INSERT … ON CONFLICT DO UPDATE … WHERE spent + reserved +
   $amount <= ceiling RETURNING`, the period's rollover in it as
   `SetTenantBudget` sets it. A row not returned is no error to PostgreSQL,
   and the batch would go on to `COMMIT` past it, so the statement wraps the
   upsert and raises when it returned none, through a function the
   migration adds that raises a SQLSTATE of limes's own; pgstore answers
   that code as `ErrTenantBudgetExceeded`. The error aborts the batch, and
   its `COMMIT` rolls back. A charge an overrun policy lets cross the
   ceiling keeps its update unconditional, as now. So the aggregate is held
   from that statement to `COMMIT`, in the same batch: one round trip, not
   eight. A `CHECK` on the table cannot do it: a tenant is past its ceiling
   legitimately once the ceiling is lowered or an overrun is charged.
3. **The one lock order becomes the hold, then counters, then copies, then
   the tenant's aggregate**, for every call that writes them — a hold
   `Settle` or `Release` ends stays first, as now — changed in one release.
   `lock_order_test.go` holds every call to it, as it holds the order now.
4. **A deadlock is retried.** A charge that PostgreSQL aborts to break a
   deadlock (40P01) is run again, once, from the start: during a rolling
   upgrade, a pgstore of the old order and one of the new run side by side,
   and their charges can wait on each other. The retry is logged. It runs
   `onCharged` again — its writes through `TxFrom` were rolled back with
   the rest — so a consumer whose `onCharged` acts outside the database
   makes that action idempotent, as it already must for a charge its own
   caller retries.
5. **What a consumer hooks in keeps its place.** A custom `Config.Scope`
   runs as now, before the read batch, one round trip more. A charge with
   `onCharged` runs it after the write batch's statements and before
   `COMMIT`, which then goes on its own, one round trip more; `TxFrom`
   still hands it the transaction, the connection-backed `pgx.Tx` of (1).
6. **The scale benchmarks hold it**: `Charge` at three round trips and
   `Bump` at three, whatever the depth, and the tenant's parallel charges
   at the added latency measured before and after.

## Options considered

### Option A: three batches, the tenant's aggregate last (chosen)

| Dimension | Assessment |
|---|---|
| Round trips | 16 → 3 for `Charge`, 10 → 3 for `Bump` |
| Tenant lock held | 8 round trips → 1 |
| Logic | Stays in Go; memstore and pgstore keep one rule |
| Risk | The lock order changes everywhere at once |

### Option B: a stored function per operation

`limes.charge(…)` in PL/pgSQL, one round trip, the locks held only for the
statement's own time.

**Pros:** the fewest round trips, the shortest locks. **Cons:** the ceiling
chain, the copies' limits, the replay and the period rollover move into SQL,
a second implementation beside memstore's Go, held to it by `metertest`
alone; `onCharged` still needs the transaction open after it. A step to
take later, measured against A, if A is not enough.

### Option C: batches with the order as it is

**Pros:** no lock-order change. **Cons:** the aggregate is locked in the
second batch and held through the third: two round trips, and every
statement between. Half the gain for the tenant.

### Option D: pgx's pipeline mode alone

**Pros:** no restructuring. **Cons:** pipelining needs the statements to be
independent; the chain's are not, so the round trips that remain are the
same ones A removes by grouping.

## Consequences

- **A tenant's throughput follows the database's speed, not its distance**:
  the lock is held for one round trip and the statements' own time. The
  scale benchmarks give the new figure before ADR-0017's leases are
  started, so the leases are sized against it.
- **`Charge` takes three round trips plus the scope and `onCharged` a
  consumer adds.** One with neither takes three.
- **The lock order is a release-wide change.** A rolling upgrade can meet
  a deadlock between versions, which the retry absorbs; the migration
  guide says so.
- **Every call that writes counters is reworked**, and `metertest`,
  `lock_order_test.go`, the soak test and the benchmarks are what hold it.

## Rollout

1. This ADR.
2. The read batch for every call, the order unchanged: the scope's
   statements go with each transaction's first, and a meter call sends the
   erase lock, the erased check, the replay's lookup and the lineage held
   (now one statement) in one batch. `Charge` went from 16 round trips to
   11, `Bump` from 10 to 6, `Reserve` to 13, held by `TestMeterRoundTrips`.
   `BEGIN` stays its own round trip: `Config.DB` offers `BeginTx` alone,
   so sending it in a batch waits for step 4, with `COMMIT`.
3. The lock order changed, the tenant's aggregate locked after the
   counters and copies by every call, with the deadlock retry: the
   aggregate is held from its lock to `COMMIT`, five round trips of
   `Charge`'s eleven, not eight. Its conditional write waits for step 4.
   A counter that was on record when a call created it can now be deleted
   while the call waits for its lock, so the call creates it again
   (`lockUsageOf`).
4. The write batch with `COMMIT`, and `onCharged` placed before it.
5. The migration guide's note on the rolling upgrade, and the benchmarks'
   figures in ADR-0017.
