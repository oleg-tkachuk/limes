package pgstore

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/link"
)

// eraseHolds is how many holds the concurrent erase check settles beside it.
const eraseHolds = 4

// meterSubject is capID of tenant as a verified capability gives it, with no
// ceiling of its own.
func meterSubject(capID, tenant uuid.UUID) limes.MeterSubject {
	return link.NewMeterSubject(link.MeterSubjectFields{
		CapabilityID: capID, TenantID: tenant, UnitCode: limes.DefaultUnitCode,
		Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true},
	}).(limes.MeterSubject)
}

// countRevocations is the revocation entries of tenant, read in tx.
const countRevocations = `SELECT count(*) FROM ` + schema + `.revocations WHERE tenant_id = $1`

// EraseTenant takes the records and the usage in one transaction and runs
// onErased in it: an error from onErased leaves everything as it was.
func TestEraseTenantTakesRecordsAndUsageTogether(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	if _, err := f.usage.Charge(ctx, limes.ChargeRequest{Subject: meterSubject(f.child.ID, f.mine), Amount: 1}, nil); err != nil {
		t.Fatal(err)
	}
	req := limes.EraseTenantRequest{TenantID: f.mine, Note: "tenant removed", Actor: "test"}
	errConsumer := errors.New("the consumer's own rows")
	if _, err := f.records.EraseTenant(ctx, req, func(context.Context, pgx.Tx) error { return errConsumer }); !errors.Is(err, errConsumer) {
		t.Fatalf("EraseTenant with a failing onErased: err = %v, want its error", err)
	}
	if r, err := f.records.GetRecord(ctx, f.child.ID); err != nil || r.Revocation != nil {
		t.Errorf("the child after a rolled-back erase = %+v, %v; want it unrevoked", r, err)
	}
	if _, err := f.usage.GetUsage(ctx, f.child.ID); err != nil {
		t.Errorf("usage after a rolled-back erase: %v", err)
	}
	var seen int64
	got, err := f.records.EraseTenant(ctx, req, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, countRevocations, f.mine).Scan(&seen)
	})
	if want := (limes.EraseTenantResult{Revoked: 1, Kept: 2}); err != nil || got != want {
		t.Fatalf("EraseTenant = %+v, %v; want %+v", got, err, want)
	}
	if seen != 2 {
		t.Errorf("onErased saw %d revocation entries of the tenant, want both", seen)
	}
	if revoked, err := f.records.IsRevoked(ctx, f.child.ID); err != nil || !revoked {
		t.Errorf("IsRevoked of the child after the erase = %v, %v; want revoked", revoked, err)
	}
	if _, err := f.usage.GetUsage(ctx, f.child.ID); !errors.Is(err, limes.ErrUsageNotFound) {
		t.Errorf("usage after the erase: err = %v, want ErrUsageNotFound", err)
	}
	if _, err := f.records.EraseTenant(WithTenant(t.Context(), f.theirs), req, nil); !errors.Is(err, limes.ErrScopeMismatch) {
		t.Errorf("EraseTenant under another tenant's scope: err = %v, want ErrScopeMismatch", err)
	}
}

// An erase locks in the meter's order, so beside settles and charges of the
// same tenant it finishes, and so do they: none fails on a deadlock.
func TestEraseTenantBesideTheMeter(t *testing.T) {
	f := newTenantFixture(t)
	ctx := WithTenant(t.Context(), f.mine)
	subject := meterSubject(f.child.ID, f.mine)
	holds := make([]limes.Reservation, eraseHolds)
	for i := range holds {
		r, err := f.usage.Reserve(ctx, limes.ReserveRequest{Subject: subject, Amount: 1})
		if err != nil {
			t.Fatal(err)
		}
		holds[i] = r
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2*eraseHolds+1)
	for _, h := range holds {
		wg.Go(func() {
			_, err := f.usage.Settle(ctx, limes.SettleRequest{Subject: subject, ReservationID: h.ID, Amount: 1}, nil)
			if !errors.Is(err, limes.ErrReservationNotFound) {
				errs <- err
			}
		})
		wg.Go(func() {
			_, err := f.usage.Charge(ctx, limes.ChargeRequest{Subject: subject, Amount: 1}, nil)
			errs <- err
		})
	}
	wg.Go(func() {
		_, err := f.records.EraseTenant(ctx, limes.EraseTenantRequest{TenantID: f.mine}, nil)
		errs <- err
	})
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("beside an erase: %v", err)
		}
	}
}
