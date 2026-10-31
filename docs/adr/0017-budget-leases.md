# ADR-0017: Budget leases for delegated capabilities

**Status:** Proposed
**Date:** 2026-10-31
**Deciders:** Oleg Tkachuk
**Relates to:** [ADR-0007](0007-pgstore-components.md), [ADR-0015](0015-release-matching-holds.md)

## Context

A charge is exact and atomic: `Charge`, `Reserve` and `Settle` lock the
counters of the capability, of every ancestor and of the tenant aggregate,
check every ceiling, write them and a ledger row in one transaction. No
ceiling is ever crossed, under any concurrency. The price is that every
charge of a tenant waits on that tenant's aggregate row, and every charge of
a delegation chain on the chain's root. An orchestrator that hands a budget
to many sub-agents, the case limes is for, puts all of their charges on one
queue.

`task bench:pgstore:scale` (pgstore/bench_scale_test.go) measures it, on
PostgreSQL in a container on the same host:

| | no added latency | +1 ms set per round trip | +2 ms set |
|---|---|---|---|
| `Charge`, one at a time | 3.0 ms | 31 ms | 51 ms |
| `Charge`, every processor, one tenant | 2.0 ms/op, ≈500/s | 21 ms/op, ≈48/s | 86 ms/op, ≈12/s |
| `IsRevoked` | 0.23 ms | 2.1 ms | 3.7 ms |

The proxy oversleeps its setting: `IsRevoked`, one round trip, shows each
round trip took about 1.9 ms and 3.5 ms longer, not 1 ms and 2 ms. The
throughput conclusion holds at either latency.

After ADR-0018, `Charge` makes 6 round trips and holds the aggregate for
three: one tenant's charges from every processor take 4.0 ms each at the
+1 ms setting (≈250/s) and 9.1 ms at +2 ms (≈110/s), instead of 21 and 86.
The queue remains, shorter: its length is now three round trips per
charge, which a lease takes off the path entirely.

Before it, `Charge` made 16 round trips and `Bump` 10, whatever the chain's depth
(1 to 64); `IsRevoked` makes one. A deeper chain costs rows, not round trips:
`Charge` takes 2.7 ms at depth 1 and 7.0 ms at depth 64. The tenant's
aggregate is locked for most of a charge's round trips, so its throughput
falls with the database's latency, and a managed database a zone away caps a
tenant at a few dozen charges a second.

Two things are at work, and they call for two answers:

- **Round trips per charge.** Fewer of them shortens every lock and every
  charge, and changes no contract: ADR-0018's work, which this rollout
  follows.
- **One queue per tenant and per chain.** However short a charge, its
  locks serialize every charge under the same root and tenant. Only taking
  those rows off the path of each charge removes the queue.

The ceilings must stay hard: a delegated budget that can be overspent is not
a budget.

## Decision

1. **A lease is budget a capability holds of its chain, in advance.**
   `meter.Leaser`, an optional interface beside `UsageStore`, with
   `Lease(ctx, LeaseRequest) (Lease, error)`. A lease holds `Amount`, as
   `Reserve` holds, against what is above the subject up to its lease's
   source: every ancestor and the tenant aggregate when no ancestor holds a
   lease, or else the ancestors up to and including the nearest one that
   does, out of that one's balance (4). It checks every ceiling on that way
   once, credits the amount to the subject as its lease balance, and lasts
   `TTL`, at most until the capability expires.
2. **A leased capability's charges stop at its lease.** A charge of a
   capability with an open lease — or of a descendant of one, up to the
   nearest leaseholder — locks and writes that capability's counters, those
   between it and the leaseholder, and the lease balance, and nothing above:
   not the ancestors the lease was drawn from, not the tenant aggregate. A
   charge the balance does not cover is refused with
   `limes.ErrBudgetExceeded`, as a ceiling refuses it; the caller leases
   more. The
   ledger row is written as now, so `SumCharges` stays exact and immediate.
3. **What a lease spent reaches the chain when it is settled.**
   `SettleLease` — on renewal, release, the capability's revocation
   (`ReleaseMatching`) or the lease's expiry (`ReleaseExpired`) — charges
   the ancestors and the tenant what the lease spent and releases the rest,
   in one transaction under the locks a charge takes now — a nested lease
   settles into its source's balance instead. Until then the
   chain holds the whole lease as reserved, which is at least what was
   spent, so no ancestor's or tenant's ceiling is crossed at any instant;
   their `SpentAmount` lags by at most one lease, and `ReservedAmount`
   covers the lag. `Refund` returns a charge to what it debited: the lease
   balance while the lease is open, and, once it is settled, the chain the
   settlement charged.
4. **Leases nest.** A descendant's lease is drawn from the nearest
   ancestor holding one, out of its balance, with no lock above it (1): an
   orchestrator leases once from the tenant, and its sub-agents from it.
5. **A lease is the meter's, not the token's.** `Delegate` and the token
   format do not change: a consumer leases for the capability it
   delegated, or not at all. A capability with no lease is charged exactly
   as now, so nothing changes for a consumer that leases nothing.
6. **memstore and pgstore implement it, and `metertest` checks it**: no
   ceiling crossed by charges under leases from every processor, the spent
   amount settled to every ancestor and the tenant, the rest released, a
   revoked or expired lease settled by housekeeping, nested leases. pgstore
   keeps a lease beside the capability's usage row (a migration).

## Options considered

### Option A: leases, settled to the chain (chosen)

| Dimension | Assessment |
|---|---|
| Throughput | A leaseholder's charges queue on its own row alone |
| Ceilings | Hard: the chain holds the lease before a charge spends it |
| Complexity | Medium: a new optional interface, a lease column set, housekeeping |
| Compatibility | Additive: no lease, no change |

**Cons:** a lease fragments the parent's budget: what one sub-agent holds
another cannot spend until it is settled. Ancestors' spend lags by a lease.

### Option B: shard the tenant aggregate

N aggregate rows per tenant, a charge picking one, a read summing them.

**Pros:** no new concept for the consumer. **Cons:** relieves the tenant
row alone, not a chain's root; a ceiling check needs every shard, so either
reads all of them under lock — the queue again — or splits the ceiling
across shards, which is a lease by another name.

### Option C: count asynchronously

Charge the capability alone, and fold the spend into ancestors and the
tenant in the background.

**Pros:** the fastest charge. **Cons:** a ceiling can be crossed by as much
as the backlog, without bound; that breaks the guarantee limes is built on.

### Option D: fewer round trips alone

**Pros:** no new concept; every charge gets cheaper; the lock is held for
fewer round trips. **Cons:** the queue stays: one tenant's charges still go
one at a time, only faster. Worth doing first, and on its own (Consequences),
but it does not remove the ceiling of one row.

## Consequences

- **An orchestrator's sub-agents charge in parallel**, each on its own
  lease, bounded by its own row and the database rather than by the
  tenant's.
- **A consumer chooses the lease size**: larger is fewer settlements and
  more fragmentation. The guide recommends a lease of a few minutes'
  expected spend, renewed when half is gone.
- **Reports differ in one place**: an ancestor's `GetUsage` shows spend up
  to the last settlement and the outstanding leases as reserved; the ledger
  and `SumCharges` are exact.
- **Round trips are reduced first, by ADR-0018**: `Charge` in one
  statement, or a pipelined batch, from 16 round trips toward one or two,
  with no change to any contract. It shortens every lock, a lease's
  settlement among them, and its benchmarks are the ones above.
- **The same lease answers `Bump`** in a later step: a request lease of N
  requests, counted on the leaseholder's row alone.

## Rollout

1. This ADR.
2. ADR-0018: fewer round trips per `Charge`, `Reserve`, `Settle` and
   `Bump`, measured by `task bench:pgstore:scale`; the steps below follow
   it.
3. `meter.Leaser`, `LeaseRequest`, `Lease` and the settlement rules, with
   `metertest` checks, in memstore.
4. pgstore: the migration, the charge path stopping at a leaseholder, and
   housekeeping settling expired and revoked leases; the scale benchmarks
   extended to charges under leases.
5. `otelmetric` forwards `Leaser`; the guide documents choosing a lease.
