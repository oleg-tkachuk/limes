package limes

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// OpenTelemetry instruments of a metered UsageStore (WithMetering).
//
// Cardinality:
//
//   - tenant_id is the one unbounded attribute, so it is opt-in
//     (WithTenantAttribute): a deployment with many tenants multiplies every
//     series by their number. Capability id is never an attribute.
//   - unit_code is bounded by ISO 4217 (IsUnitCode). Amounts are only
//     meaningful per unit: summing USD and EUR is not a number, so every
//     amount carries its unit and no instrument has a fixed unit of its own.
//   - outcome is one of the outcome* constants below.

// instrumentationScope names the meter the instruments are created on.
const instrumentationScope = "github.com/oleg-tkachuk/limes"

// Instrument names, as an exporter shows them.
const (
	metricChargeAmount    = "limes.charge.amount"
	metricChargeDecisions = "limes.charge.decisions"
	metricRequestBumps    = "limes.request.bumps"
	metricRefundAmount    = "limes.refund.amount"
	metricReservations    = "limes.reservation.decisions"
	metricCurrentSpend    = "limes.charge.current_spend"
)

// Attribute keys.
const (
	attrOutcome  = "outcome"
	attrTenantID = "tenant_id"
	attrUnitCode = "unit_code"
)

// Outcomes of a metered call.
const (
	// outcomeAllowed — every ceiling admitted it.
	outcomeAllowed = "allowed"
	// outcomeOverrun — committed past a ceiling under OverrunRecord.
	outcomeOverrun = "overrun"
	// outcomeCapExceeded — a capability's, an ancestor's or a copy's ceiling
	// refused it.
	outcomeCapExceeded = "cap_exceeded"
	// outcomeTenantExceeded — the tenant aggregate refused it.
	outcomeTenantExceeded = "tenant_exceeded"
	// outcomeLimitExceeded — a request limit refused a Bump.
	outcomeLimitExceeded = "limit_exceeded"
	// outcomeError — it failed for any other reason: a malformed request, a
	// failed onCharged, a store error.
	outcomeError = "error"
)

// MeteringOption configures WithMetering.
type MeteringOption func(*meteringConfig)

type meteringConfig struct {
	provider metric.MeterProvider
	tenant   bool
}

// WithMeterProvider creates the instruments on p rather than on the global
// provider (otel.GetMeterProvider).
func WithMeterProvider(p metric.MeterProvider) MeteringOption {
	return func(c *meteringConfig) { c.provider = p }
}

// WithTenantAttribute labels every measurement with tenant_id. Off by
// default: it multiplies each series by the number of tenants.
func WithTenantAttribute() MeteringOption {
	return func(c *meteringConfig) { c.tenant = true }
}

// meteringInstruments are one metered store's instruments.
type meteringInstruments struct {
	tenant bool

	chargeAmount    metric.Float64Counter   // total charged, by unit
	chargeDecisions metric.Int64Counter     // charge attempts, by outcome
	requestBumps    metric.Int64Counter     // bump attempts, by outcome
	refundAmount    metric.Float64Counter   // total refunded, by unit
	reservations    metric.Int64Counter     // reservation attempts, by outcome
	currentSpend    metric.Float64Histogram // capability spend after each charge
}

func newMeteringInstruments(cfg meteringConfig) (*meteringInstruments, error) {
	provider := cfg.provider
	if provider == nil {
		provider = otel.GetMeterProvider()
	}
	meter := provider.Meter(instrumentationScope)
	in := &meteringInstruments{tenant: cfg.tenant}
	var errs [6]error
	in.chargeAmount, errs[0] = meter.Float64Counter(metricChargeAmount,
		metric.WithDescription("Amount charged against capability budgets, by unit_code."))
	in.chargeDecisions, errs[1] = meter.Int64Counter(metricChargeDecisions,
		metric.WithDescription("Charge and settle attempts, by outcome."))
	in.requestBumps, errs[2] = meter.Int64Counter(metricRequestBumps,
		metric.WithDescription("Request-count bumps, by outcome."))
	in.refundAmount, errs[3] = meter.Float64Counter(metricRefundAmount,
		metric.WithDescription("Amount refunded from recorded charges, by unit_code."))
	in.reservations, errs[4] = meter.Int64Counter(metricReservations,
		metric.WithDescription("Reservation attempts, by outcome."))
	in.currentSpend, errs[5] = meter.Float64Histogram(metricCurrentSpend,
		metric.WithDescription("A capability's spend after each charge, by unit_code."))
	if err := errors.Join(errs[:]...); err != nil {
		return nil, fmt.Errorf("limes: metering instruments: %w", err)
	}
	return in, nil
}

// attrs is the attribute set of one measurement: the tenant when enabled,
// then kv.
func (in *meteringInstruments) attrs(tenantID uuid.UUID, kv ...attribute.KeyValue) metric.MeasurementOption {
	if in.tenant && tenantID != uuid.Nil {
		kv = append(kv, attribute.String(attrTenantID, tenantID.String()))
	}
	return metric.WithAttributes(kv...)
}

// chargeOutcome classifies a charge or settle that was not replayed.
func chargeOutcome(receipt ChargeReceipt, err error) string {
	switch {
	case err == nil && receipt.Overrun:
		return outcomeOverrun
	case err == nil:
		return outcomeAllowed
	case errors.Is(err, ErrTenantBudgetExceeded):
		return outcomeTenantExceeded
	case errors.Is(err, ErrBudgetExceeded):
		return outcomeCapExceeded
	default:
		return outcomeError
	}
}

// recordCharge counts one charge or settle attempt and, when it committed,
// its amount and the capability's spend after it, in the receipt's unit. The
// instruments count float64, the metrics contract; amounts are exact up to
// that boundary.
func (in *meteringInstruments) recordCharge(ctx context.Context, tenantID uuid.UUID, amount Nanos, receipt ChargeReceipt, err error) {
	outcome := chargeOutcome(receipt, err)
	in.chargeDecisions.Add(ctx, 1, in.attrs(tenantID, attribute.String(attrOutcome, outcome)))
	if err != nil {
		return
	}
	unit := in.attrs(receipt.TenantID, attribute.String(attrUnitCode, receipt.UnitCode))
	in.chargeAmount.Add(ctx, amount.Float64(), unit)
	in.currentSpend.Record(ctx, receipt.Spent.Float64(), unit)
}

// recordBump counts one Bump attempt.
func (in *meteringInstruments) recordBump(ctx context.Context, tenantID uuid.UUID, err error) {
	outcome := outcomeAllowed
	switch {
	case errors.Is(err, ErrRequestLimitExceeded):
		outcome = outcomeLimitExceeded
	case err != nil:
		outcome = outcomeError
	}
	in.requestBumps.Add(ctx, 1, in.attrs(tenantID, attribute.String(attrOutcome, outcome)))
}

// recordRefund counts one committed refund, in the charge's unit.
func (in *meteringInstruments) recordRefund(ctx context.Context, r RefundReceipt) {
	in.refundAmount.Add(ctx, r.Refunded.Float64(), in.attrs(r.TenantID, attribute.String(attrUnitCode, r.UnitCode)))
}

// recordReservation counts one Reserve attempt.
func (in *meteringInstruments) recordReservation(ctx context.Context, tenantID uuid.UUID, err error) {
	outcome := chargeOutcome(ChargeReceipt{}, err)
	in.reservations.Add(ctx, 1, in.attrs(tenantID, attribute.String(attrOutcome, outcome)))
}
