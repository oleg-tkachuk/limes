// Package otelmetric wraps a meter.UsageStore in OpenTelemetry instruments:
// charge, settle, refund, reservation and request-count measurements, by
// outcome and unit, and a capability's spend after each charge. It is a
// module of its own so that limes itself never resolves an OpenTelemetry
// dependency; a consumer that wants the metrics adds this one.
//
// The outcome attribute is one of allowed; overrun, committed past a
// ceiling under meter.OverrunPolicyRecord; budget_exceeded;
// tenant_budget_exceeded; request_limit_exceeded, a request limit or a
// Biscuit copy past its quota (meter.ErrCopyLimitExceeded), on a Bump,
// Charge, Settle or Reserve alike; and error.
//
//	usage, err := otelmetric.Instrument(store,
//		otelmetric.WithMeterProvider(provider), // default: the global provider
//		otelmetric.WithTenantAttribute(),       // opt-in: one series per tenant
//	)
package otelmetric
