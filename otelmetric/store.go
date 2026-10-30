package otelmetric

import (
	"context"
	"fmt"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// meteringStore is a meter.UsageStore decorator that emits OTel metrics on every
// counter mutation, so the store implementation stays free of metrics
// concerns and every meter.UsageStore user gets the same observability surface.
// Cardinality discipline lives in metrics.go.
type meteringStore struct {
	inner meter.UsageStore
	in    *meteringInstruments
}

// Instrument wraps a meter.UsageStore in OpenTelemetry instruments. Its instruments
// are created on the global MeterProvider unless WithMeterProvider names
// another, and carry no tenant unless WithTenantAttribute asks for it.
//
// The result implements every interface of this package that inner does —
// the optional meter.CopyUsageReader, meter.TenantUsageEraser and
// meter.MatchingReleaser — so wrapping a store never hides what it can do. A
// decorator forwards a fixed list of optional interfaces, and a test fails
// when the list misses one the store has.
func Instrument(inner meter.UsageStore, opts ...Option) (meter.UsageStore, error) {
	if inner == nil {
		return nil, fmt.Errorf("%w: otelmetric: Instrument needs a meter.UsageStore", limes.ErrInvalidConfig)
	}
	var cfg meteringConfig
	for _, o := range opts {
		o(&cfg)
	}
	in, err := newMeteringInstruments(cfg)
	if err != nil {
		return nil, err
	}
	return withOptional(&meteringStore{inner: inner, in: in}, inner), nil
}

// withOptional is m, which wraps inner, implementing each optional
// interface inner does, and none it does not: one case per mix.
func withOptional(m *meteringStore, inner meter.UsageStore) meter.UsageStore {
	reader, reads := inner.(meter.CopyUsageReader)
	eraser, erases := inner.(meter.TenantUsageEraser)
	releaser, releases := inner.(meter.MatchingReleaser)
	r, e, l := copyUsageReader{reader}, tenantUsageEraser{eraser}, matchingReleaser{releaser}
	switch {
	case reads && erases && releases:
		return &struct {
			*meteringStore
			copyUsageReader
			tenantUsageEraser
			matchingReleaser
		}{m, r, e, l}
	case reads && erases:
		return &struct {
			*meteringStore
			copyUsageReader
			tenantUsageEraser
		}{m, r, e}
	case reads && releases:
		return &struct {
			*meteringStore
			copyUsageReader
			matchingReleaser
		}{m, r, l}
	case erases && releases:
		return &struct {
			*meteringStore
			tenantUsageEraser
			matchingReleaser
		}{m, e, l}
	case reads:
		return &struct {
			*meteringStore
			copyUsageReader
		}{m, r}
	case erases:
		return &struct {
			*meteringStore
			tenantUsageEraser
		}{m, e}
	case releases:
		return &struct {
			*meteringStore
			matchingReleaser
		}{m, l}
	}
	return m
}

// copyUsageReader forwards CopyUsage. Reading moves nothing, so it emits no
// metric.
type copyUsageReader struct {
	copies meter.CopyUsageReader
}

func (c copyUsageReader) CopyUsage(ctx context.Context, req meter.CopyUsageRequest) (meter.CopyUsageResult, error) {
	return c.copies.CopyUsage(ctx, req)
}

// tenantUsageEraser forwards EraseTenantUsage. Erasing moves no counter a
// metric follows, so it emits none.
type tenantUsageEraser struct {
	eraser meter.TenantUsageEraser
}

func (e tenantUsageEraser) EraseTenantUsage(ctx context.Context, req meter.EraseTenantUsageRequest) error {
	return e.eraser.EraseTenantUsage(ctx, req)
}

// matchingReleaser forwards ReleaseMatching. Releasing holds emits nothing,
// as ReleaseExpired does not.
type matchingReleaser struct {
	releaser meter.MatchingReleaser
}

func (l matchingReleaser) ReleaseMatching(ctx context.Context, req meter.ReleaseMatchingRequest) (meter.ReleaseMatchingResult, error) {
	return l.releaser.ReleaseMatching(ctx, req)
}

// Bump emits limes.request.bumps.
func (s *meteringStore) Bump(ctx context.Context, req meter.BumpRequest) (meter.BumpReceipt, error) {
	receipt, err := s.inner.Bump(ctx, req)
	s.in.recordBump(ctx, req.Subject.TenantID(), err)
	return receipt, err
}

// Charge emits limes.charge.{decisions,amount,current_spend}. A replay moved
// nothing, so it counts nothing: counting it would report spend twice.
func (s *meteringStore) Charge(ctx context.Context, req meter.ChargeRequest, onCharged func(ctx context.Context, c meter.Charged) error) (meter.ChargeReceipt, error) {
	receipt, err := s.inner.Charge(ctx, req, onCharged)
	if err == nil && receipt.Replayed {
		return receipt, nil
	}
	s.in.recordCharge(ctx, req.Subject.TenantID(), req.Amount, receipt, err)
	return receipt, err
}

// Refund emits limes.refund.amount.
func (s *meteringStore) Refund(ctx context.Context, req meter.RefundRequest) (meter.RefundReceipt, error) {
	r, err := s.inner.Refund(ctx, req)
	if err == nil && r.Amount > 0 {
		s.in.recordRefund(ctx, r)
	}
	return r, err
}

// Reserve emits limes.reservation.decisions.
func (s *meteringStore) Reserve(ctx context.Context, req meter.ReserveRequest) (meter.Reservation, error) {
	r, err := s.inner.Reserve(ctx, req)
	s.in.recordReservation(ctx, req.Subject.TenantID(), err)
	return r, err
}

// Settle is a charge as far as the metrics are concerned, attributed to the
// subject's tenant as a charge is: a refused settle has no receipt to name it.
func (s *meteringStore) Settle(ctx context.Context, req meter.SettleRequest, onCharged func(ctx context.Context, c meter.Charged) error) (meter.ChargeReceipt, error) {
	receipt, err := s.inner.Settle(ctx, req, onCharged)
	if err == nil && receipt.Replayed {
		return receipt, nil
	}
	s.in.recordCharge(ctx, req.Subject.TenantID(), req.Amount, receipt, err)
	return receipt, err
}

// Pure pass-throughs — reads and admin writes don't move counters, no metric.

func (s *meteringStore) GetReservation(ctx context.Context, reservationID uuid.UUID) (meter.Reservation, error) {
	return s.inner.GetReservation(ctx, reservationID)
}

func (s *meteringStore) ListReservations(ctx context.Context, req meter.ListReservationsRequest) (limes.Page[meter.Reservation], error) {
	return s.inner.ListReservations(ctx, req)
}

func (s *meteringStore) GetCharge(ctx context.Context, chargeID uuid.UUID) (meter.ChargeRecord, error) {
	return s.inner.GetCharge(ctx, chargeID)
}

func (s *meteringStore) ChargeByRef(ctx context.Context, req meter.ChargeByRefRequest) (meter.ChargeRecord, error) {
	return s.inner.ChargeByRef(ctx, req)
}

func (s *meteringStore) GetUsage(ctx context.Context, capID uuid.UUID) (meter.Usage, error) {
	return s.inner.GetUsage(ctx, capID)
}

func (s *meteringStore) GetTenantBudget(ctx context.Context, tenantID uuid.UUID) (meter.TenantBudget, error) {
	return s.inner.GetTenantBudget(ctx, tenantID)
}

func (s *meteringStore) SetTenantBudget(ctx context.Context, args meter.SetTenantBudgetRequest) (meter.TenantBudget, error) {
	return s.inner.SetTenantBudget(ctx, args)
}

func (s *meteringStore) ListTenantBudgets(ctx context.Context, args meter.ListTenantBudgetsRequest) (limes.Page[meter.TenantBudget], error) {
	return s.inner.ListTenantBudgets(ctx, args)
}

func (s *meteringStore) DeleteUsage(ctx context.Context, req meter.DeleteUsageRequest) error {
	return s.inner.DeleteUsage(ctx, req)
}

func (s *meteringStore) PurgeOrphans(ctx context.Context, req meter.PurgeOrphansRequest) (meter.PurgeOrphansResult, error) {
	return s.inner.PurgeOrphans(ctx, req)
}

func (s *meteringStore) Release(ctx context.Context, req meter.ReleaseRequest) error {
	return s.inner.Release(ctx, req)
}

func (s *meteringStore) ReleaseExpired(ctx context.Context, req meter.ReleaseExpiredRequest) (meter.ReleaseExpiredResult, error) {
	return s.inner.ReleaseExpired(ctx, req)
}
