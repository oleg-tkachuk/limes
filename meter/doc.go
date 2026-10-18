// Package meter counts what a verified capability uses — requests and spend,
// against the capability's ceilings, its ancestors', its Biscuit copies' and
// its tenant's — and keeps the charges ledger, for billing and reports.
//
// limes verifies a capability and hands the meter what to count it against,
// [limes.VerifiedCapability.MeterSubject]; nothing in limes depends on this
// package, so a consumer that only verifies tokens never imports it. The
// refusals a ceiling answers with — [limes.ErrRequestLimitExceeded],
// [limes.ErrBudgetExceeded], [limes.ErrTenantBudgetExceeded] — are limes's,
// so [limes.Verifier.ErrorResponse] answers a metering refusal as it answers
// a verification one.
//
// # What you implement
//
// [UsageStore] — request and spend counters and the charges ledger; the
// union of [Meter], [MeterReader], [TenantBudgets] and [UsageHousekeeping].
// Every charge and request also counts against each ancestor; see
// "Transactions" below. More
// are needed only with the feature they serve: [CopyUsageReader] to show a
// Biscuit copy's own counters, [LedgerReader] to sum a tenant's charges, and
// [TenantUsageEraser] to remove a tenant. memstore ships them in memory,
// pgstore over PostgreSQL, and metertest checks any implementation.
//
// # Transactions
//
// Meter.Charge and Meter.Settle take an onCharged callback that runs inside
// the charge, receiving the charge's receipt (Charged) and a context that
// carries the store's transaction, so a side effect such as an outbox write
// commits atomically with the spend and names the charge. The store puts
// its transaction there and reads it back with an accessor of its own —
// pgstore.TxFrom — so neither the contract nor a caller holding a
// UsageStore names a transaction type, and one service can meter through
// stores of different kinds. That is what keeps this package free of any
// database dependency.
//
// A store with no transactions, such as memstore, hands onCharged the
// caller's context. Everything else behaves identically; only the
// atomic-side-effect guarantee is forgone, and that limitation is
// store-dependent rather than a degradation of the primitive.
package meter
