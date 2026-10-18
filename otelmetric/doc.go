// Package otelmetric wraps a meter.UsageStore in OpenTelemetry instruments:
// charge, settle, refund, reservation and request-count measurements, by
// outcome and unit, and a capability's spend after each charge. It is a
// module of its own so that limes itself never resolves an OpenTelemetry
// dependency; a consumer that wants the metrics adds this one.
//
//	usage, err := otelmetric.Instrument(store,
//		otelmetric.WithMeterProvider(provider), // default: the global provider
//		otelmetric.WithTenantAttribute(),       // opt-in: one series per tenant
//	)
package otelmetric
