# ADR-0002: Bound the charges ledger before 1.0

**Status:** Proposed
**Date:** 2026-10-24
**Deciders:** Oleg Tkachuk

## Context

Every committed charge is a row of `limes.charges` (pgstore,
`migrations/002_meter.sql`) or of memstore's ledger, and nothing removes one
but erasing its tenant (`EraseTenant`). The schema says so: the ledger
"outlives" the records and the counters. A service that meters every agent
call writes one row per call, for ever; the table and its indexes
(`charges_external_ref`, `charges_reservation`, `charges_tenant_at`) grow with
it, and so do `SumCharges`' reads across tenants.

A ledger row is not only history. Four contracts read it back:

| Contract | Reads the row | Today's promise |
|---|---|---|
| `Meter.Charge` with `ExternalRef` | `charges_external_ref`, one row per (capability, reference) | Idempotent on the pair, "also once the capability is no longer on record" (`meter/usage.go:119-125`) |
| `Meter.Settle` | `charges_reservation`, one row per settled hold | A settled hold settles again as a replay |
| `Meter.Refund` | the row by id, its `refunded`, `ancestors` and `copies` | Refunds any recorded charge; an unknown id is `ErrChargeNotFound` |
| `LedgerReader.SumCharges` | rows in `[From, To)` by `at` | A refund moves the sums of its charge's period, whenever it is made |

A row removed early therefore does not only shorten a report: a retried
charge under the same reference is taken a second time, and a late refund of
it fails. Today no promise says how long a reference or a charge stays
answerable, because none needs to: rows are never removed.

At 1.0 the API freezes until `/v2` ([ADR-0001](0001-architecture-before-1-0.md)).
Two different things are at stake, and only one of them is frozen:

- **The interfaces.** `UsageHousekeeping` is part of `meter.UsageStore`,
  which consumers implement; a method added to it after 1.0 breaks them. A
  new optional interface, asserted on the store as `LedgerReader` and
  `CopyUsageReader` are, can be added in any minor.
- **The promises.** "Idempotent for ever" and "refundable for ever" are
  documented behaviour. Shortening them after 1.0 is a breaking change of
  behaviour even with no signature changed; widening them later is not.

## Decision

Before 1.0, state the windows in the contracts, and leave the mechanism to a
minor:

1. Reword the promises of `Charge` (with `ExternalRef`), `Settle` and `Refund`
   to hold **while the charge is in the ledger**, and say that a ledger may
   remove charges older than a retention the consumer chooses, no shorter
   than `meter.MinChargeRetention`.
2. Add `meter.MinChargeRetention`, the floor a store refuses a retention
   under: long enough for any retry and any refund a consumer's own process
   allows, so the default use never meets the window. Its value is decided
   with the wording (a proposal: 400 days — a year of billing plus a month's
   disputes).
3. After 1.0, in a minor: an optional `meter.LedgerHousekeeping` interface,
   `PurgeCharges(ctx, PurgeChargesRequest{Retention, Limit})
   (PurgeChargesResult, error)`, batched like `Store.Purge`, implemented by
   memstore and pgstore and checked by `metertest` — including that a
   reference past the retention charges anew and a refund of a purged charge
   is `ErrChargeNotFound`, as the reworded contracts say.

## Options considered

### Option A: leave retention to the consumer, documented

The guide says the ledger grows, and how to delete old rows in SQL.

| Dimension | Assessment |
|---|---|
| Complexity | None in the library |
| Risk | High: a hand-written `DELETE` breaks idempotency and refunds silently, against a contract that promises both for ever |
| Reversibility | High |

**Pros:** no code. **Cons:** every consumer works out the same constraints
alone, and the contracts keep promising what a deletion breaks.

### Option B: reword the contracts now, purge in a minor (chosen)

| Dimension | Assessment |
|---|---|
| Complexity | Low now (wording, one constant); medium later (one optional interface, two stores, metertest) |
| Risk to 1.0 | None: no signature changes |
| Reversibility | The window can be widened in any minor; only narrowing would break |

**Pros:** the frozen part is settled before the freeze; the mechanism lands
when a consumer needs it, tested by the shared suite. **Cons:** until it
lands, a consumer who must bound the table still writes its own deletion,
now at least against a contract that allows it.

### Option C: partition `limes.charges` by time in pgstore

Declarative partitions by `at`, old partitions detached and dropped.

| Dimension | Assessment |
|---|---|
| Complexity | High: a migration that rewrites the largest table |
| Feasibility | A unique index on a partitioned table must include the partition key, so `charges_external_ref (capability_id, external_ref)` and `charges_reservation (reservation_id)` would be unique only within a partition: a retry across a partition boundary would charge twice. The primary key and `Refund`'s lookup by id would need `at` too |
| Reversibility | Low |

**Pros:** dropping a partition is cheap, and old data leaves without a long
`DELETE`. **Cons:** it breaks the idempotency it would have to keep, unless
the uniqueness moves to a table of its own, which is option B's work plus a
rewrite. Worth revisiting only if `PurgeCharges`' batched deletes prove too
slow at a consumer's volume.

### Option D: roll up old charges into period sums

Keep `SumCharges` exact for ever by folding purged rows into per-period,
per-group totals.

**Pros:** reports outlive the rows. **Cons:** `SumCharges` groups by tenant,
operation, actor and capability and filters by them, so a rollup that keeps
every query exact keeps nearly every row. A consumer who needs long reports
copies the ledger to its warehouse, which option B's retention leaves room
for. Not pursued.

## Trade-off analysis

The discriminating force is the freeze, and it falls on the promises, not on
the mechanism. Option A keeps a promise every deletion breaks; option C
breaks idempotency to save delete cost; option D solves reports, which are
not the problem. Option B settles the one thing 1.0 makes expensive — what a
consumer may rely on — and defers what any minor can add.

## Consequences

- The contracts of `Charge`, `Settle` and `Refund` name the ledger as their
  window; the guide says the ledger grows until a store purges it, and that
  a deletion of one's own must respect `MinChargeRetention` too.
- `metertest` gains no check until the purge lands; then it checks the
  window from both sides.
- To revisit: option C, if batched purges cannot keep up; option D, if a
  consumer needs exact sums past the retention inside limes.

## Action items

1. [ ] Before 1.0: reword `Meter.Charge`, `Meter.Settle`, `Meter.Refund` and
   `LedgerReader` to hold while the charge is in the ledger; add
   `meter.MinChargeRetention` with its value; a guide paragraph on the
   ledger's growth.
2. [ ] After 1.0, when a consumer asks: `meter.LedgerHousekeeping` with
   `PurgeCharges`, memstore and pgstore implementations, `metertest` checks
   of the window, a pgstore plan test that the purge reads by `at`.
