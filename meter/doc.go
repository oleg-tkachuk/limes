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
// Every charge and request also counts against each ancestor. Generic in TX,
// the consumer's transaction handle; see "No-transaction mode" below. More
// are needed only with the feature they serve: [CopyUsageReader] to show a
// Biscuit copy's own counters, [LedgerReader] to sum a tenant's charges, and
// [TenantUsageEraser] to remove a tenant. memstore ships them in memory,
// pgstore over PostgreSQL, and metertest checks any implementation.
//
// # No-transaction mode
//
// Meter.Charge and Meter.Settle take an onCharged callback that runs inside
// the charge, receiving the consumer's transaction handle and the charge's
// receipt (Charged), so a side effect such as an outbox write commits
// atomically with the spend and names the charge. The module never
// constructs, inspects or constrains that handle — it only threads it back.
// That is what keeps this package free of any database dependency.
//
// A consumer with no transactional storage instantiates UsageStore[struct{}]
// and always passes nil for onCharged. Everything else behaves identically;
// only the atomic-side-effect guarantee is forgone, and that limitation is
// store-dependent rather than a degradation of the primitive.
package meter
