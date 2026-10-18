package otelmetric

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// meteringStore is a meter.UsageStore decorator that emits OTel metrics on every
// counter mutation, so the store implementation stays free of metrics
// concerns and every meter.UsageStore user gets the same observability surface.
// Cardinality discipline lives in metrics.go.
type meteringStore[TX any] struct {
	inner meter.UsageStore[TX]
	in    *meteringInstruments
}

// Instrument wraps a meter.UsageStore in OpenTelemetry instruments. Its instruments
// are created on the global MeterProvider unless WithMeterProvider names
// another, and carry no tenant unless WithTenantAttribute asks for it.
//
// The result implements every interface of this package that inner does —
// the optional meter.CopyUsageReader and meter.TenantUsageEraser — so wrapping a store
// never hides what it can do. A decorator forwards a fixed list of optional
// interfaces, and a test fails when the list misses one the store has.
func Instrument[TX any](inner meter.UsageStore[TX], opts ...Option) (meter.UsageStore[TX], error) {
	if inner == nil {
		return nil, errors.New("limes: Instrument needs a meter.UsageStore")
	}
	var cfg meteringConfig
	for _, o := range opts {
		o(&cfg)
	}
	in, err := newMeteringInstruments(cfg)
	if err != nil {
		return nil, err
	}
	m := &meteringStore[TX]{inner: inner, in: in}
	reader, reads := inner.(meter.CopyUsageReader)
	eraser, erases := inner.(meter.TenantUsageEraser)
	switch {
	case reads && erases:
		return &meteringCopyErasingStore[TX]{
			meteringCopyStore: &meteringCopyStore[TX]{meteringStore: m, copies: reader},
			tenantUsageEraser: tenantUsageEraser{eraser},
		}, nil
	case reads:
		return &meteringCopyStore[TX]{meteringStore: m, copies: reader}, nil
	case erases:
		return &meteringErasingStore[TX]{meteringStore: m, tenantUsageEraser: tenantUsageEraser{eraser}}, nil
	}
	return m, nil
}

// tenantUsageEraser forwards EraseTenantUsage. Erasing moves no counter a
// metric follows, so it emits none.
type tenantUsageEraser struct {
	eraser meter.TenantUsageEraser
}

func (e tenantUsageEraser) EraseTenantUsage(ctx context.Context, req meter.EraseTenantUsageRequest) error {
	return e.eraser.EraseTenantUsage(ctx, req)
}

// meteringErasingStore is meteringStore over a store that also erases a
// tenant's usage.
type meteringErasingStore[TX any] struct {
	*meteringStore[TX]
	tenantUsageEraser
}

// meteringCopyErasingStore is meteringCopyStore over a store that also
// erases a tenant's usage.
type meteringCopyErasingStore[TX any] struct {
	*meteringCopyStore[TX]
	tenantUsageEraser
}

// meteringCopyStore is meteringStore over a store that also reads Biscuit
// copies' counters. Reading moves nothing, so it emits no metric.
type meteringCopyStore[TX any] struct {
	*meteringStore[TX]
	copies meter.CopyUsageReader
}

func (s *meteringCopyStore[TX]) CopyUsage(ctx context.Context, req meter.CopyUsageRequest) (meter.CopyUsageResult, error) {
	return s.copies.CopyUsage(ctx, req)
}

// Bump emits limes.request.bumps.
func (s *meteringStore[TX]) Bump(ctx context.Context, req meter.BumpRequest) (meter.BumpReceipt, error) {
	receipt, err := s.inner.Bump(ctx, req)
	s.in.recordBump(ctx, req.Subject.TenantID(), err)
	return receipt, err
}

// Charge emits limes.charge.{decisions,amount,current_spend}. A replay moved
// nothing, so it counts nothing: counting it would report spend twice.
func (s *meteringStore[TX]) Charge(ctx context.Context, req meter.ChargeRequest, onCharged func(ctx context.Context, c meter.Charged[TX]) error) (meter.ChargeReceipt, error) {
	receipt, err := s.inner.Charge(ctx, req, onCharged)
	if err == nil && receipt.Replayed {
		return receipt, nil
	}
	s.in.recordCharge(ctx, req.Subject.TenantID(), req.Amount, receipt, err)
	return receipt, err
}

// Refund emits limes.refund.amount.
func (s *meteringStore[TX]) Refund(ctx context.Context, req meter.RefundRequest) (meter.RefundReceipt, error) {
	r, err := s.inner.Refund(ctx, req)
	if err == nil && r.Amount > 0 {
		s.in.recordRefund(ctx, r)
	}
	return r, err
}

// Reserve emits limes.reservation.decisions.
func (s *meteringStore[TX]) Reserve(ctx context.Context, req meter.ReserveRequest) (meter.Reservation, error) {
	r, err := s.inner.Reserve(ctx, req)
	s.in.recordReservation(ctx, req.Subject.TenantID(), err)
	return r, err
}

// Settle is a charge as far as the metrics are concerned, attributed to the
// subject's tenant as a charge is: a refused settle has no receipt to name it.
func (s *meteringStore[TX]) Settle(ctx context.Context, req meter.SettleRequest, onCharged func(ctx context.Context, c meter.Charged[TX]) error) (meter.ChargeReceipt, error) {
	receipt, err := s.inner.Settle(ctx, req, onCharged)
	if err == nil && receipt.Replayed {
		return receipt, nil
	}
	s.in.recordCharge(ctx, req.Subject.TenantID(), req.Amount, receipt, err)
	return receipt, err
}

// Pure pass-throughs — reads and admin writes don't move counters, no metric.

func (s *meteringStore[TX]) GetReservation(ctx context.Context, reservationID uuid.UUID) (meter.Reservation, error) {
	return s.inner.GetReservation(ctx, reservationID)
}

func (s *meteringStore[TX]) ListReservations(ctx context.Context, req meter.ListReservationsRequest) (limes.Page[meter.Reservation], error) {
	return s.inner.ListReservations(ctx, req)
}

func (s *meteringStore[TX]) GetCharge(ctx context.Context, chargeID uuid.UUID) (meter.ChargeRecord, error) {
	return s.inner.GetCharge(ctx, chargeID)
}

func (s *meteringStore[TX]) ChargeByRef(ctx context.Context, req meter.ChargeByRefRequest) (meter.ChargeRecord, error) {
	return s.inner.ChargeByRef(ctx, req)
}

func (s *meteringStore[TX]) GetUsage(ctx context.Context, capID uuid.UUID) (meter.Usage, error) {
	return s.inner.GetUsage(ctx, capID)
}

func (s *meteringStore[TX]) GetTenantBudget(ctx context.Context, tenantID uuid.UUID) (meter.TenantBudget, error) {
	return s.inner.GetTenantBudget(ctx, tenantID)
}

func (s *meteringStore[TX]) SetTenantBudget(ctx context.Context, args meter.SetTenantBudgetRequest) (meter.TenantBudget, error) {
	return s.inner.SetTenantBudget(ctx, args)
}

func (s *meteringStore[TX]) ListTenantBudgets(ctx context.Context, args meter.ListTenantBudgetsRequest) (limes.Page[meter.TenantBudget], error) {
	return s.inner.ListTenantBudgets(ctx, args)
}

func (s *meteringStore[TX]) DeleteUsage(ctx context.Context, req meter.DeleteUsageRequest) error {
	return s.inner.DeleteUsage(ctx, req)
}

func (s *meteringStore[TX]) PurgeOrphans(ctx context.Context, req meter.PurgeOrphansRequest) (meter.PurgeOrphansResult, error) {
	return s.inner.PurgeOrphans(ctx, req)
}

func (s *meteringStore[TX]) Release(ctx context.Context, req meter.ReleaseRequest) error {
	return s.inner.Release(ctx, req)
}

func (s *meteringStore[TX]) ReleaseExpired(ctx context.Context, req meter.ReleaseExpiredRequest) (meter.ReleaseExpiredResult, error) {
	return s.inner.ReleaseExpired(ctx, req)
}
