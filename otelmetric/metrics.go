package otelmetric

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// OpenTelemetry instruments of a metered meter.UsageStore (Instrument).
//
// Cardinality:
//
//   - limes.tenant_id is the one unbounded attribute, so it is opt-in
//     (WithTenantAttribute): a deployment with many tenants multiplies every
//     series by their number. Capability id is never an attribute.
//   - limes.unit_code is bounded by ISO 4217 (limes.IsUnitCode). Amounts are only
//     meaningful per unit: summing USD and EUR is not a number, so every
//     amount carries its unit and no instrument has a fixed unit of its own.
//   - limes.outcome is one of the outcome* constants below.
//   - limes.scope, on a refusal for a limit only, is one of the scope*
//     constants below: which ceiling refused it.
//
// Every key is under limes., so none meets another library's outcome or
// tenant_id on the same series.

// instrumentationScope names the meter the instruments are created on.
const instrumentationScope = "github.com/oleg-tkachuk/limes/otelmetric"

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
	attrOutcome  = "limes.outcome"
	attrScope    = "limes.scope"
	attrTenantID = "limes.tenant_id"
	attrUnitCode = "limes.unit_code"
)

// Outcomes of a metered call.
const (
	// outcomeAllowed — every ceiling admitted it.
	outcomeAllowed = "allowed"
	// outcomeOverrun — committed past a ceiling under meter.OverrunPolicyRecord.
	outcomeOverrun = "overrun"
	// outcomeCapExceeded — a budget ceiling refused it: a capability's, an
	// ancestor's or a lease's. The refusals carry their Reason's name, so a dashboard and
	// a refused response say the same thing.
	outcomeCapExceeded = string(limes.ReasonBudgetExceeded)
	// outcomeTenantExceeded — the tenant aggregate refused it.
	outcomeTenantExceeded = string(limes.ReasonTenantBudgetExceeded)
	// outcomeLimitExceeded — a request limit, a lease's requests or a copy
	// quota refused the call.
	outcomeLimitExceeded = string(limes.ReasonRequestLimitExceeded)
	// outcomeError — it failed for any other reason: a malformed request, a
	// failed onCharged, a store error.
	outcomeError = "error"
)

// Scopes of a refusal for a limit: where the ceiling that refused it sits.
const (
	// scopeCapability — the capability's ceiling or an ancestor's.
	scopeCapability = "capability"
	// scopeLease — the lease the capability, or an ancestor, holds.
	scopeLease = "lease"
	// scopeCopy — a Biscuit copy's quota.
	scopeCopy = "copy"
	// scopeTenant — the tenant aggregate.
	scopeTenant = "tenant"
)

// refusalScope is which ceiling refused err, and false for an error that is
// no refusal for a limit. The narrower sentinels come first: each matches
// the wider family a capability's own refusal is.
func refusalScope(err error) (string, bool) {
	switch {
	case errors.Is(err, meter.ErrLeaseExhausted), errors.Is(err, meter.ErrLeaseRequestsExhausted):
		return scopeLease, true
	case errors.Is(err, meter.ErrCopyLimitExceeded):
		return scopeCopy, true
	case errors.Is(err, limes.ErrTenantBudgetExceeded):
		return scopeTenant, true
	case errors.Is(err, limes.ErrBudgetExceeded), errors.Is(err, limes.ErrRequestLimitExceeded):
		return scopeCapability, true
	default:
		return "", false
	}
}

// outcomeAttrs is the attributes of an attempt's outcome: limes.outcome,
// and limes.scope when a limit refused it.
func outcomeAttrs(outcome string, err error) []attribute.KeyValue {
	kv := []attribute.KeyValue{attribute.String(attrOutcome, outcome)}
	if scope, ok := refusalScope(err); ok {
		kv = append(kv, attribute.String(attrScope, scope))
	}
	return kv
}

// Option configures Instrument.
type Option func(*meteringConfig)

type meteringConfig struct {
	provider metric.MeterProvider
	tenant   bool
}

// WithMeterProvider creates the instruments on p rather than on the global
// provider (otel.GetMeterProvider).
func WithMeterProvider(p metric.MeterProvider) Option {
	return func(c *meteringConfig) { c.provider = p }
}

// WithTenantAttribute labels every measurement with limes.tenant_id. Off by
// default: it multiplies each series by the number of tenants.
func WithTenantAttribute() Option {
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

// providerOf is cfg's provider: the global one when none was set.
func providerOf(cfg meteringConfig) metric.MeterProvider {
	if cfg.provider == nil {
		return otel.GetMeterProvider()
	}
	return cfg.provider
}

func newMeteringInstruments(cfg meteringConfig) (*meteringInstruments, error) {
	meter := providerOf(cfg).Meter(instrumentationScope)
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
		metric.WithDescription("meter.Reservation attempts, by outcome."))
	in.currentSpend, errs[5] = meter.Float64Histogram(metricCurrentSpend,
		metric.WithDescription("A capability's spend after each charge, by unit_code."))
	if err := errors.Join(errs[:]...); err != nil {
		return nil, fmt.Errorf("otelmetric: instruments: %w", err)
	}
	return in, nil
}

// attrs is the attribute set of one measurement: the tenant when enabled,
// then kv. Every measurement names one, as every verified subject does.
func (in *meteringInstruments) attrs(tenantID uuid.UUID, kv ...attribute.KeyValue) metric.MeasurementOption {
	if in.tenant {
		kv = append(kv, attribute.String(attrTenantID, tenantID.String()))
	}
	return metric.WithAttributes(kv...)
}

// chargeOutcome classifies a charge or settle that was not replayed.
func chargeOutcome(receipt meter.ChargeReceipt, err error) string {
	switch {
	case err == nil && receipt.Overrun:
		return outcomeOverrun
	case err == nil:
		return outcomeAllowed
	case errors.Is(err, limes.ErrTenantBudgetExceeded):
		return outcomeTenantExceeded
	case errors.Is(err, limes.ErrBudgetExceeded):
		return outcomeCapExceeded
	case errors.Is(err, limes.ErrRequestLimitExceeded):
		return outcomeLimitExceeded
	default:
		return outcomeError
	}
}

// recordCharge counts one charge or settle attempt and, when it committed,
// its amount and the capability's spend after it, in the receipt's unit. The
// instruments count float64, the metrics contract; amounts are exact up to
// that boundary.
func (in *meteringInstruments) recordCharge(ctx context.Context, tenantID uuid.UUID, amount limes.Nanos, receipt meter.ChargeReceipt, err error) {
	outcome := chargeOutcome(receipt, err)
	in.chargeDecisions.Add(ctx, 1, in.attrs(tenantID, outcomeAttrs(outcome, err)...))
	if err != nil {
		return
	}
	unit := in.attrs(tenantID, attribute.String(attrUnitCode, receipt.UnitCode))
	in.chargeAmount.Add(ctx, amount.Float64(), unit)
	in.currentSpend.Record(ctx, receipt.SpentAmount.Float64(), unit)
}

// recordBump counts one Bump attempt.
func (in *meteringInstruments) recordBump(ctx context.Context, tenantID uuid.UUID, err error) {
	outcome := outcomeAllowed
	switch {
	case errors.Is(err, limes.ErrRequestLimitExceeded):
		outcome = outcomeLimitExceeded
	case err != nil:
		outcome = outcomeError
	}
	in.requestBumps.Add(ctx, 1, in.attrs(tenantID, outcomeAttrs(outcome, err)...))
}

// recordRefund counts one committed refund, in the charge's unit.
func (in *meteringInstruments) recordRefund(ctx context.Context, r meter.RefundReceipt) {
	in.refundAmount.Add(ctx, r.Amount.Float64(), in.attrs(r.TenantID, attribute.String(attrUnitCode, r.UnitCode)))
}

// recordReservation counts one Reserve attempt.
func (in *meteringInstruments) recordReservation(ctx context.Context, tenantID uuid.UUID, err error) {
	outcome := chargeOutcome(meter.ChargeReceipt{}, err)
	in.reservations.Add(ctx, 1, in.attrs(tenantID, outcomeAttrs(outcome, err)...))
}
