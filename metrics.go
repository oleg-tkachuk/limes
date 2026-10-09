package limes

import (
	"context"
	"sync"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// OpenTelemetry instruments for the capability runtime counters,
// created lazily so a consumer that never wires a MeterProvider (tests,
// sidecar tools) does not pay for instrument creation.
//
// Cardinality:
//
//   - tenant_id is the only high-cardinality label. Capability id is
//     deliberately omitted — a deployment with thousands of capabilities
//     would blow the exporter's series count.
//   - unit_code is a small closed set (AllowedUnitCodes). Amounts are
//     only meaningful per unit: summing USD and EUR is not a number, so
//     every amount instrument carries the unit as an attribute and has
//     no fixed metric unit of its own.
//   - outcome is a bounded enum: allowed / cap_exceeded / tenant_exceeded
//     for charges, allowed / limit_exceeded for request bumps.
var (
	capMetricsOnce sync.Once

	capChargeAmount    metric.Float64Counter   // total charged, by tenant + unit
	capChargeDecisions metric.Int64Counter     // allow/deny per attempt
	capRequestBumps    metric.Int64Counter     // allow/deny per attempt
	capRefundAmount    metric.Float64Counter   // total refunded
	capReservations    metric.Int64Counter     // allow/deny per reservation
	capCurrentSpend    metric.Float64Histogram // distribution of post-charge spend
)

func initMetrics() {
	capMetricsOnce.Do(func() {
		meter := otel.Meter("github.com/oleg-tkachuk/limes")

		capChargeAmount, _ = meter.Float64Counter(
			"limes.charge.amount",
			metric.WithDescription("Cumulative amount charged against capability budgets, by tenant and unit_code."),
		)
		capChargeDecisions, _ = meter.Int64Counter(
			"limes.charge.decisions",
			metric.WithDescription("Charge attempts bucketed by outcome: allowed / cap_exceeded / tenant_exceeded."),
		)
		capRequestBumps, _ = meter.Int64Counter(
			"limes.request.bumps",
			metric.WithDescription("Per-capability request-count bumps, bucketed by outcome: allowed / limit_exceeded."),
		)
		capRefundAmount, _ = meter.Float64Counter(
			"limes.refund.amount",
			metric.WithDescription("Cumulative amount refunded from recorded charges."),
		)
		capReservations, _ = meter.Int64Counter(
			"limes.reservation.decisions",
			metric.WithDescription("Reservation attempts bucketed by outcome: allowed / cap_exceeded / tenant_exceeded."),
		)
		capCurrentSpend, _ = meter.Float64Histogram(
			"limes.charge.current_spend",
			metric.WithDescription("Per-capability spend after each successful charge, by tenant and unit_code."),
		)
	})
}

func tenantAttrs(tenantID uuid.UUID, attrs ...attribute.KeyValue) []attribute.KeyValue {
	if tenantID != uuid.Nil {
		attrs = append(attrs, attribute.String("tenant_id", tenantID.String()))
	}
	return attrs
}

// recordChargeAttempt is called once per Charge regardless of outcome. The
// instruments count units as float64, the metrics contract; the amounts are
// exact up to that boundary.
func recordChargeAttempt(ctx context.Context, tenantID uuid.UUID, unit string, amount, postSpend Nanos, outcome string) {
	initMetrics()
	if capChargeDecisions == nil {
		return
	}
	capChargeDecisions.Add(ctx, 1, metric.WithAttributes(
		tenantAttrs(tenantID, attribute.String("outcome", outcome))...))
	if outcome != "allowed" {
		return
	}
	var amountOpts []attribute.KeyValue
	if unit != "" {
		amountOpts = append(amountOpts, attribute.String("unit_code", unit))
	}
	amountAttrs := metric.WithAttributes(tenantAttrs(tenantID, amountOpts...)...)
	if capChargeAmount != nil {
		capChargeAmount.Add(ctx, amount.Float64(), amountAttrs)
	}
	if capCurrentSpend != nil {
		capCurrentSpend.Record(ctx, postSpend.Float64(), amountAttrs)
	}
}

// recordRequestBump is called once per Bump regardless of outcome.
func recordRequestBump(ctx context.Context, tenantID uuid.UUID, outcome string) {
	initMetrics()
	if capRequestBumps == nil {
		return
	}
	capRequestBumps.Add(ctx, 1, metric.WithAttributes(
		tenantAttrs(tenantID, attribute.String("outcome", outcome))...))
}

// recordRefund is called once per committed refund. The refund request names
// only the charge, so neither tenant nor unit is known here without a lookup
// this decorator deliberately does not make.
func recordRefund(ctx context.Context, amount Nanos) {
	initMetrics()
	if capRefundAmount == nil {
		return
	}
	capRefundAmount.Add(ctx, amount.Float64())
}

// recordReservation is called once per Reserve regardless of outcome.
func recordReservation(ctx context.Context, tenantID uuid.UUID, outcome string) {
	initMetrics()
	if capReservations == nil {
		return
	}
	capReservations.Add(ctx, 1, metric.WithAttributes(
		tenantAttrs(tenantID, attribute.String("outcome", outcome))...))
}
