// Package otelmetric wraps a meter.UsageStore in OpenTelemetry instruments:
// charge, settle, refund, reservation and request-count measurements, by
// outcome and unit, and a capability's spend after each charge. It is a
// module of its own so that limes itself never resolves an OpenTelemetry
// dependency; a consumer that wants the metrics adds this one.
//
// The limes.outcome attribute is one of allowed; overrun, committed past a
// ceiling under meter.OverrunPolicyRecord; budget_exceeded;
// tenant_budget_exceeded; request_limit_exceeded, a request limit or a
// Biscuit copy past its quota (meter.ErrCopyLimitExceeded), on a Bump,
// Charge, Settle or Reserve alike; and error. A refusal for a limit also
// carries limes.scope, which ceiling refused it: capability (its own or an
// ancestor's), lease, copy or tenant.
//
// The instruments: limes.charge.amount and limes.refund.amount, by
// limes.unit_code; limes.charge.decisions (charges and settles),
// limes.reservation.decisions and limes.request.bumps, by limes.outcome and
// limes.scope; and limes.charge.current_spend, a histogram of a
// capability's spend after each charge, by limes.unit_code. A limit refuses
// on whichever counter the call it refused counts on — a Bump on
// limes.request.bumps, a Charge or Settle on limes.charge.decisions, a
// Reserve on limes.reservation.decisions — so an alert on every limit reads
// all three, by limes.scope.
//
//	usage, err := otelmetric.Instrument(store,
//		otelmetric.WithMeterProvider(provider), // default: the global provider
//		otelmetric.WithTenantAttribute(),       // opt-in: one series per tenant
//	)
//
// InstrumentRevocationCache reports a limes.RevocationCache's counts —
// hits, misses, upstream lookups, evictions and entries, each by kind
// (capability or biscuit), its clears, whole and narrow, and the revocation
// signal: whether lineage and a signal vouch for it, the signal's
// connections, losses and notifications — as observable instruments,
// read only when the provider collects:
//
//	reg, err := otelmetric.InstrumentRevocationCache(cache,
//		otelmetric.WithMeterProvider(provider))
//	defer reg.Unregister()
package otelmetric
