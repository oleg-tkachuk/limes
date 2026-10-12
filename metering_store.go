package limes

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// meteringStore is a UsageStore decorator that emits OTel metrics on every
// counter mutation, so the store implementation stays free of metrics
// concerns and every UsageStore user gets the same observability surface.
// Cardinality discipline lives in metrics.go.
type meteringStore[TX any] struct {
	inner UsageStore[TX]
	in    *meteringInstruments
}

// Instrument wraps a UsageStore in OpenTelemetry instruments. Its instruments
// are created on the global MeterProvider unless WithMeterProvider names
// another, and carry no tenant unless WithTenantAttribute asks for it.
//
// The result implements every interface of this package that inner does —
// today the one optional interface, CopyUsageReader — so wrapping a store
// never hides what it can do. A decorator forwards a fixed list of optional
// interfaces, and a test fails when the list misses one the store has.
func Instrument[TX any](inner UsageStore[TX], opts ...InstrumentOption) (UsageStore[TX], error) {
	if inner == nil {
		return nil, errors.New("limes: Instrument needs a UsageStore")
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
	if reader, ok := inner.(CopyUsageReader); ok {
		return &meteringCopyStore[TX]{meteringStore: m, copies: reader}, nil
	}
	return m, nil
}

// meteringCopyStore is meteringStore over a store that also reads Biscuit
// copies' counters. Reading moves nothing, so it emits no metric.
type meteringCopyStore[TX any] struct {
	*meteringStore[TX]
	copies CopyUsageReader
}

func (s *meteringCopyStore[TX]) CopyUsage(ctx context.Context, revocationIDs [][]byte) ([]CopyUsage, error) {
	return s.copies.CopyUsage(ctx, revocationIDs)
}

// Bump emits limes.request.bumps.
func (s *meteringStore[TX]) Bump(ctx context.Context, req BumpRequest) (BumpReceipt, error) {
	receipt, err := s.inner.Bump(ctx, req)
	s.in.recordBump(ctx, req.Subject.TenantID(), err)
	return receipt, err
}

// Charge emits limes.charge.{decisions,amount,current_spend}. A replay moved
// nothing, so it counts nothing: counting it would report spend twice.
func (s *meteringStore[TX]) Charge(ctx context.Context, req ChargeRequest, onCharged func(ctx context.Context, tx TX) error) (ChargeReceipt, error) {
	receipt, err := s.inner.Charge(ctx, req, onCharged)
	if err == nil && receipt.Replayed {
		return receipt, nil
	}
	s.in.recordCharge(ctx, req.Subject.TenantID(), req.Amount, receipt, err)
	return receipt, err
}

// Refund emits limes.refund.amount.
func (s *meteringStore[TX]) Refund(ctx context.Context, req RefundRequest) (RefundReceipt, error) {
	r, err := s.inner.Refund(ctx, req)
	if err == nil && r.RefundedAmount > 0 {
		s.in.recordRefund(ctx, r)
	}
	return r, err
}

// Reserve emits limes.reservation.decisions.
func (s *meteringStore[TX]) Reserve(ctx context.Context, req ReserveRequest) (Reservation, error) {
	r, err := s.inner.Reserve(ctx, req)
	s.in.recordReservation(ctx, req.Subject.TenantID(), err)
	return r, err
}

// Settle is a charge as far as the metrics are concerned. Its request names
// only the reservation; the receipt carries the tenant and the unit.
func (s *meteringStore[TX]) Settle(ctx context.Context, req SettleRequest, onCharged func(ctx context.Context, tx TX) error) (ChargeReceipt, error) {
	receipt, err := s.inner.Settle(ctx, req, onCharged)
	if err == nil && receipt.Replayed {
		return receipt, nil
	}
	s.in.recordCharge(ctx, receipt.TenantID, req.Amount, receipt, err)
	return receipt, err
}

// Pure pass-throughs — reads and admin writes don't move counters, no metric.

func (s *meteringStore[TX]) GetReservation(ctx context.Context, reservationID uuid.UUID) (Reservation, error) {
	return s.inner.GetReservation(ctx, reservationID)
}

func (s *meteringStore[TX]) ListReservations(ctx context.Context, req ListReservationsRequest) (Page[Reservation], error) {
	return s.inner.ListReservations(ctx, req)
}

func (s *meteringStore[TX]) GetCharge(ctx context.Context, chargeID uuid.UUID) (ChargeRecord, error) {
	return s.inner.GetCharge(ctx, chargeID)
}

func (s *meteringStore[TX]) ChargeByRef(ctx context.Context, req ChargeByRefRequest) (ChargeRecord, error) {
	return s.inner.ChargeByRef(ctx, req)
}

func (s *meteringStore[TX]) GetUsage(ctx context.Context, capID uuid.UUID) (Usage, error) {
	return s.inner.GetUsage(ctx, capID)
}

func (s *meteringStore[TX]) GetTenantBudget(ctx context.Context, tenantID uuid.UUID) (TenantBudget, error) {
	return s.inner.GetTenantBudget(ctx, tenantID)
}

func (s *meteringStore[TX]) SetTenantBudget(ctx context.Context, args SetTenantBudgetRequest) (TenantBudget, error) {
	return s.inner.SetTenantBudget(ctx, args)
}

func (s *meteringStore[TX]) ListTenantBudgets(ctx context.Context, args ListTenantBudgetsRequest) (Page[TenantBudget], error) {
	return s.inner.ListTenantBudgets(ctx, args)
}

func (s *meteringStore[TX]) DeleteUsage(ctx context.Context, req DeleteUsageRequest) error {
	return s.inner.DeleteUsage(ctx, req)
}

func (s *meteringStore[TX]) PurgeOrphans(ctx context.Context, req PurgeOrphansRequest) (PurgeOrphansResult, error) {
	return s.inner.PurgeOrphans(ctx, req)
}

func (s *meteringStore[TX]) Release(ctx context.Context, req ReleaseRequest) error {
	return s.inner.Release(ctx, req)
}

func (s *meteringStore[TX]) ReleaseExpired(ctx context.Context, req ReleaseExpiredRequest) (ReleaseExpiredResult, error) {
	return s.inner.ReleaseExpired(ctx, req)
}
