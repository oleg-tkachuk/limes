package otelmetric

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"go.opentelemetry.io/otel/metric"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// InstrumentRequests wraps a meter.RequestCounter in limes.request.bumps
// alone: the instrument, attributes and options Instrument counts a Bump
// with, so a dashboard reads one series whichever wrapper a binary uses.
// Its instruments are created on the global MeterProvider unless
// WithMeterProvider names another, and carry no tenant unless
// WithTenantAttribute asks for it.
//
// Use it where the code only counts requests. Instrument wraps every method
// of a meter.UsageStore and its optional interfaces, and a binary that
// reflects on methods by name, as text/template does, keeps every one of
// them and what they call, whether or not the binary calls it; this wrapper
// has one method. The result is a meter.RequestCounter and nothing more:
// none of inner's other interfaces reach through it.
func InstrumentRequests(inner meter.RequestCounter, opts ...Option) (meter.RequestCounter, error) {
	if inner == nil {
		return nil, fmt.Errorf("%w: otelmetric: InstrumentRequests needs a meter.RequestCounter", limes.ErrInvalidConfig)
	}
	var cfg meteringConfig
	for _, o := range opts {
		o(&cfg)
	}
	in, err := newBumpInstruments(providerOf(cfg).Meter(instrumentationScope), labels{tenant: cfg.tenant})
	if err != nil {
		return nil, fmt.Errorf("otelmetric: instruments: %w", err)
	}
	return &meteringRequests{inner: inner, in: in}, nil
}

// meteringRequests is InstrumentRequests' decorator.
type meteringRequests struct {
	inner meter.RequestCounter
	in    *bumpInstruments
}

// Bump emits limes.request.bumps, as Instrument's Bump does.
func (r *meteringRequests) Bump(ctx context.Context, req meter.BumpRequest, onBumped func(ctx context.Context, b meter.Bumped) error) (meter.BumpReceipt, error) {
	return r.in.bump(ctx, r.inner, req, onBumped)
}

// bumpInstruments are the instruments of a Bump, which Instrument and
// InstrumentRequests share.
type bumpInstruments struct {
	labels
	requestBumps metric.Int64Counter // bump attempts, by outcome
}

// newBumpInstruments creates limes.request.bumps on m.
func newBumpInstruments(m metric.Meter, l labels) (*bumpInstruments, error) {
	bumps, err := m.Int64Counter(metricRequestBumps, metric.WithDescription("Request-count bumps, by outcome."))
	if err != nil {
		return nil, err
	}
	return &bumpInstruments{labels: l, requestBumps: bumps}, nil
}

// bump bumps req on inner and counts the attempt. A replay counted
// nothing, so it counts nothing here either, as a replayed charge does not.
func (in *bumpInstruments) bump(ctx context.Context, inner meter.RequestCounter, req meter.BumpRequest, onBumped func(ctx context.Context, b meter.Bumped) error) (meter.BumpReceipt, error) {
	receipt, err := inner.Bump(ctx, req, onBumped)
	if err == nil && receipt.Replayed {
		return receipt, nil
	}
	in.recordBump(ctx, req.Subject.TenantID(), err)
	return receipt, err
}

// recordBump counts one Bump attempt.
func (in *bumpInstruments) recordBump(ctx context.Context, tenantID uuid.UUID, err error) {
	outcome := outcomeAllowed
	switch {
	case errors.Is(err, limes.ErrRequestLimitExceeded):
		outcome = outcomeLimitExceeded
	case err != nil:
		outcome = outcomeError
	}
	in.requestBumps.Add(ctx, 1, in.attrs(tenantID, outcomeAttrs(outcome, err)...))
}
