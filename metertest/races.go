package metertest

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/expect"
	"github.com/oleg-tkachuk/limes/meter"
)

// The checks below race concurrentDeliveries calls that each promise to move
// one thing once — a refund, a settle, a release, a top-up, an erase, a
// versioned write — and hold the store to the outcome the contract names,
// whichever call wins. A store that reads, decides and writes in separate
// steps lets more than one of them through.

// Amounts the races move, in the suite's unit.
const (
	// racedCharge is the charge partial refunds of one race to return:
	// half as many fit as arrive.
	racedCharge = concurrentDeliveries / 2 * one
	// keptCharge is a second charge on the same counters, which a refund
	// returned twice would eat into.
	keptCharge = one
	// racedHold is the hold the settle and release races end; settledCost
	// is what a settle of it charges.
	racedHold   = 3 * one
	settledCost = 2 * one
	// keptHold is a second hold on the same counters, which a release
	// returned twice would eat into.
	keptHold = 2 * one
	// raceRounds is how many holds the settle-and-release race ends, each
	// raced once, so its outcome is not one coin flip.
	raceRounds = concurrentDeliveries / 2
	// racedLease is the lease the top-up race starts from, and racedTopUp
	// what each top-up adds: the child's ceiling fits a whole number of them.
	racedLease = 4 * one
	racedTopUp = 2 * one
)

// tally counts the outcomes of raced calls.
type tally struct {
	mu       sync.Mutex
	accepted int
	refused  int
}

func (c *tally) accept() { c.mu.Lock(); c.accepted++; c.mu.Unlock() }
func (c *tally) refuse() { c.mu.Lock(); c.refused++; c.mu.Unlock() }

// holdsAndSpends holds the child, the root and the tenant to spending spent
// and holding held, after what.
func holdsAndSpends(t *testing.T, f fixture, what string, spent, held limes.Nanos) {
	t.Helper()
	for _, id := range []uuid.UUID{f.child, f.root} {
		u := f.usageOf(t, id)
		expect.That(t, u.SpentAmount == spent && u.ReservedAmount == held, "%s: %s spent %v and holds %v, want %v and %v", what, id, u.SpentAmount, u.ReservedAmount, spent, held)
	}
	b := f.tenantOf(t)
	expect.That(t, b.SpentAmount == spent && b.ReservedAmount == held, "%s: the tenant spent %v and holds %v, want %v and %v", what, b.SpentAmount, b.ReservedAmount, spent, held)
}

// Partial refunds of one charge racing to its last unit return no more than
// it took: as many as fit are refunded, every other is
// ErrRefundExceedsCharge, and every counter keeps the other charge.
func checkConcurrentRefunds(t *testing.T, f fixture) {
	_, err := f.charge(meter.ChargeRequest{Amount: keptCharge}, nil)
	expect.Must(t, err == nil, "charge: %v", err)
	c, err := f.charge(meter.ChargeRequest{Amount: racedCharge}, nil)
	expect.Must(t, err == nil, "charge: %v", err)
	var (
		wg  sync.WaitGroup
		got tally
	)
	for range concurrentDeliveries {
		wg.Go(func() {
			_, err := f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: c.ChargeID, Amount: one}, nil)
			switch {
			case err == nil:
				got.accept()
			case errors.Is(err, meter.ErrRefundExceedsCharge):
				got.refuse()
			default:
				t.Errorf("refund: %v", err)
			}
		})
	}
	wg.Wait()
	want := int(racedCharge / one)
	expect.That(t, got.accepted == want, "%d refunds of 1 accepted from a charge of %v, want %d", got.accepted, racedCharge, want)
	holdsAndSpends(t, f, "refunds raced to a charge's last unit", keptCharge, 0)
}

// Full refunds of one charge at once return it once: what their receipts
// return sums to the charge, and every counter keeps the other charge.
func checkConcurrentFullRefunds(t *testing.T, f fixture) {
	_, err := f.charge(meter.ChargeRequest{Amount: keptCharge}, nil)
	expect.Must(t, err == nil, "charge: %v", err)
	c, err := f.charge(meter.ChargeRequest{Amount: racedCharge}, nil)
	expect.Must(t, err == nil, "charge: %v", err)
	var (
		wg       sync.WaitGroup
		returned atomic.Int64
	)
	for range concurrentDeliveries {
		wg.Go(func() {
			r, err := f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: c.ChargeID, Remaining: true}, nil)
			if err != nil {
				t.Errorf("full refund: %v", err)
				return
			}
			returned.Add(int64(r.Amount))
		})
	}
	wg.Wait()
	got := limes.Nanos(returned.Load())
	expect.That(t, got == racedCharge, "full refunds at once returned %v of a charge of %v", got, racedCharge)
	holdsAndSpends(t, f, "full refunds at once", keptCharge, 0)
}

// Deliveries of one keyed Refund at once refund once: one is fresh, every
// other a replay of it, and onRefunded runs once.
func checkConcurrentKeyedRefunds(t *testing.T, f fixture) {
	c := f.keyedCharged(t)
	req := meter.RefundRequest{ChargeID: c.ChargeID, Amount: keyedPart, RefundID: uuid.New()}
	var (
		wg        sync.WaitGroup
		fresh     atomic.Int64
		callbacks atomic.Int64
	)
	onRefunded := func(context.Context, meter.Refunded) error {
		callbacks.Add(1)
		return nil
	}
	for range concurrentDeliveries {
		wg.Go(func() {
			r, err := f.Usage.Refund(f.Ctx, req, onRefunded)
			if err != nil || r.Amount != keyedPart {
				t.Errorf("keyed refund = %+v, %v; want %v returned", r, err, keyedPart)
				return
			}
			if !r.Replayed {
				fresh.Add(1)
			}
		})
	}
	wg.Wait()
	expect.That(t, fresh.Load() == 1, "%d fresh refunds under one id, want one", fresh.Load())
	expect.That(t, callbacks.Load() == 1, "onRefunded ran %d times for one keyed refund, want once", callbacks.Load())
	holdsAndSpends(t, f, "one keyed refund delivered at once", keyedCharge-keyedPart, 0)
}

// Settles of one hold at once charge it once: one receipt is fresh, every
// other a replay of the same charge, and onCharged runs once.
func checkConcurrentSettles(t *testing.T, f fixture) {
	res, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: racedHold})
	expect.Must(t, err == nil, "reserve: %v", err)
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		fresh     int
		charges   = map[uuid.UUID]bool{}
		callbacks atomic.Int64
	)
	onCharged := func(context.Context, meter.Charged) error {
		callbacks.Add(1)
		return nil
	}
	for range concurrentDeliveries {
		wg.Go(func() {
			r, err := f.Usage.Settle(f.Ctx, meter.SettleRequest{Subject: f.childSubject(), ReservationID: res.ID, Amount: settledCost}, onCharged)
			if err != nil || r.SpentAmount != settledCost {
				t.Errorf("settle = %+v, %v; want %v charged", r, err, settledCost)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			charges[r.ChargeID] = true
			if !r.Replayed {
				fresh++
			}
		})
	}
	wg.Wait()
	expect.That(t, fresh == 1, "%d fresh settles of one hold, want one", fresh)
	expect.That(t, len(charges) == 1, "settles of one hold named %d charges, want one", len(charges))
	expect.That(t, callbacks.Load() == 1, "onCharged ran %d times for one settle, want once", callbacks.Load())
	holdsAndSpends(t, f, "one hold settled at once", settledCost, 0)
}

// Releases of one hold at once return it once: every other hold stays held.
func checkConcurrentReleases(t *testing.T, f fixture) {
	_, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: keptHold})
	expect.Must(t, err == nil, "reserve: %v", err)
	res, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: racedHold})
	expect.Must(t, err == nil, "reserve: %v", err)
	var wg sync.WaitGroup
	for range concurrentDeliveries {
		wg.Go(func() {
			err := f.Usage.Release(f.Ctx, meter.ReleaseRequest{Subject: f.childSubject(), ReservationID: res.ID})
			if err != nil {
				t.Errorf("release: %v", err)
			}
		})
	}
	wg.Wait()
	_, err = f.Usage.GetReservation(f.Ctx, res.ID)
	expect.That(t, errors.Is(err, meter.ErrReservationNotFound), "GetReservation of a released hold: err = %v, want ErrReservationNotFound", err)
	holdsAndSpends(t, f, "one hold released at once", 0, keptHold)
}

// A Settle and a Release of one hold at once end it once: either the
// settle charges and the release finds nothing to do, or the release frees
// it and the settle finds no reservation. Nothing stays held, and what is
// spent is what the settles that won charged.
func checkSettleRacingRelease(t *testing.T, f fixture) {
	var settled atomic.Int64
	for range raceRounds {
		res, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: one})
		expect.Must(t, err == nil, "reserve: %v", err)
		var wg sync.WaitGroup
		wg.Go(func() {
			_, err := f.Usage.Settle(f.Ctx, meter.SettleRequest{Subject: f.childSubject(), ReservationID: res.ID, Amount: one}, nil)
			switch {
			case err == nil:
				settled.Add(1)
			case !errors.Is(err, meter.ErrReservationNotFound):
				t.Errorf("settle racing a release: %v", err)
			}
		})
		wg.Go(func() {
			if err := f.Usage.Release(f.Ctx, meter.ReleaseRequest{Subject: f.childSubject(), ReservationID: res.ID}); err != nil {
				t.Errorf("release racing a settle: %v", err)
			}
		})
		wg.Wait()
		_, err = f.Usage.GetReservation(f.Ctx, res.ID)
		expect.That(t, errors.Is(err, meter.ErrReservationNotFound), "GetReservation of a hold settled or released: err = %v, want ErrReservationNotFound", err)
	}
	holdsAndSpends(t, f, "settles racing releases", limes.Nanos(settled.Load())*one, 0)
}

// Top-ups of one lease at once are each held to the ceilings once: as many
// as fit are accepted, every other is refused by the ceiling, and the lease
// holds what the accepted ones added on every counter above it; settled, it
// leaves nothing held.
func checkConcurrentLeaseTopUps(t *testing.T, f fixture) {
	l := f.leaser(t)
	child := f.childSubject()
	f.lease(t, l, child, racedLease)
	var (
		wg  sync.WaitGroup
		got tally
	)
	for range concurrentDeliveries {
		wg.Go(func() {
			_, err := l.Lease(f.Ctx, meter.LeaseRequest{Subject: child, Amount: racedTopUp})
			switch {
			case err == nil:
				got.accept()
			case errors.Is(err, limes.ErrBudgetExceeded):
				got.refuse()
			default:
				t.Errorf("lease top-up: %v", err)
			}
		})
	}
	wg.Wait()
	want := int((childBudget - racedLease) / racedTopUp)
	expect.That(t, got.accepted == want, "%d top-ups of %v accepted from a lease of %v under a ceiling of %v, want %d", got.accepted, racedTopUp, racedLease, childBudget, want)
	open, err := l.GetLease(f.Ctx, f.child)
	expect.Must(t, err == nil, "GetLease: %v", err)
	expect.That(t, open.Amount == childBudget && open.Spent == 0, "the lease after the top-ups = %+v, want %v leased", open, childBudget)
	r, b := f.usageOf(t, f.root), f.tenantOf(t)
	expect.That(t, r.ReservedAmount == childBudget, "the root holds %v under the topped-up lease, want %v", r.ReservedAmount, childBudget)
	expect.That(t, b.ReservedAmount == childBudget, "the tenant holds %v under the topped-up lease, want %v", b.ReservedAmount, childBudget)
	_, err = l.SettleLease(f.Ctx, meter.SettleLeaseRequest{Subject: child})
	expect.Must(t, err == nil, "SettleLease: %v", err)
	holdsAndSpends(t, f, "the topped-up lease settled", 0, 0)
}

// Charges racing EraseTenantUsage either commit before it, and go with the
// tenant, or are refused as ErrTenantErased: none commits after it, and
// nothing of the tenant is left.
func checkEraseRacingCharges(t *testing.T, f fixture) {
	eraser, ok := f.Usage.(meter.TenantUsageEraser)
	if !ok {
		t.Skip("the UsageStore does not implement meter.TenantUsageEraser")
	}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		charged []uuid.UUID
		erased  meter.EraseTenantUsageResult
	)
	for i := range concurrentDeliveries {
		if i == concurrentDeliveries/2 {
			wg.Go(func() {
				var err error
				erased, err = eraser.EraseTenantUsage(f.Ctx, meter.EraseTenantUsageRequest{TenantID: f.Tenant})
				if err != nil {
					t.Errorf("EraseTenantUsage: %v", err)
				}
			})
		}
		wg.Go(func() {
			r, err := f.charge(meter.ChargeRequest{Amount: one}, nil)
			switch {
			case err == nil:
				mu.Lock()
				charged = append(charged, r.ChargeID)
				mu.Unlock()
			case !errors.Is(err, limes.ErrTenantErased):
				t.Errorf("charge racing an erase: %v", err)
			}
		})
	}
	wg.Wait()
	expect.That(t, erased.Charges == int64(len(charged)), "EraseTenantUsage removed %d charges while %d committed, want them all", erased.Charges, len(charged))
	for _, id := range charged {
		_, err := f.Usage.GetCharge(f.Ctx, id)
		expect.That(t, errors.Is(err, meter.ErrChargeNotFound), "a charge that committed racing the erase is still on record: %v", err)
	}
	for _, id := range []uuid.UUID{f.root, f.child} {
		u, err := f.Usage.GetUsage(f.Ctx, id)
		expect.That(t, errors.Is(err, meter.ErrUsageNotFound), "usage of %s after charges racing the erase = %+v, %v; want ErrUsageNotFound", id, u, err)
	}
	_, err := f.Usage.GetTenantBudget(f.Ctx, f.Tenant)
	expect.That(t, errors.Is(err, meter.ErrTenantBudgetNotFound), "GetTenantBudget after charges racing the erase: err = %v, want ErrTenantBudgetNotFound", err)
	_, err = f.charge(meter.ChargeRequest{Amount: one}, nil)
	expect.That(t, errors.Is(err, limes.ErrTenantErased), "a charge after the erase: err = %v, want ErrTenantErased", err)
}

// Writes of the tenant's ceiling at once, each expecting the version they
// read, apply once: one is accepted, every other is
// ErrTenantBudgetVersionMismatch, and the ceiling is the accepted one's.
func checkConcurrentBudgetWrites(t *testing.T, f fixture) {
	b := f.tenantOf(t)
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		winner []limes.Nanos
	)
	for i := range concurrentDeliveries {
		ceiling := tenantBudget + limes.Nanos(i+1)*one
		wg.Go(func() {
			_, err := f.Usage.SetTenantBudget(f.Ctx, meter.SetTenantBudgetRequest{
				TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Max: ceiling, UnitCode: unit}, ExpectedVersion: b.ResourceVersion,
			})
			switch {
			case err == nil:
				mu.Lock()
				winner = append(winner, ceiling)
				mu.Unlock()
			case !errors.Is(err, meter.ErrTenantBudgetVersionMismatch):
				t.Errorf("SetTenantBudget at version %d: %v", b.ResourceVersion, err)
			}
		})
	}
	wg.Wait()
	expect.Must(t, len(winner) == 1, "%d writes at one version accepted (%v), want one", len(winner), winner)
	got := f.tenantOf(t).Ceiling
	expect.That(t, got == (limes.BudgetCeiling{Max: winner[0], UnitCode: unit}), "the ceiling after writes at one version = %v, want the accepted %v", got, winner[0])
}

// endingCallbackPause is how long the callback of
// checkEndingCallsBesideAReset takes once it has sent a reset: long enough
// for a reset that does not wait for the call to commit meanwhile.
const endingCallbackPause = 100 * time.Millisecond

// endingCall makes what it ends, then ends it, running pause inside its
// callback.
type endingCall struct {
	setup func(t *testing.T, f fixture) any
	end   func(f fixture, made any, pause func()) error
}

// endingCalls are the calls with a callback that end what a Reserve or a
// Charge made.
var endingCalls = map[string]endingCall{
	"a Settle": {
		setup: func(t *testing.T, f fixture) any {
			h, err := f.Usage.Reserve(f.Ctx, meter.ReserveRequest{Subject: f.childSubject(), Amount: cent})
			expect.Must(t, err == nil, "reserve: %v", err)
			return h.ID
		},
		end: func(f fixture, made any, pause func()) error {
			_, err := f.Usage.Settle(f.Ctx, meter.SettleRequest{Subject: f.childSubject(), ReservationID: made.(uuid.UUID), Amount: cent},
				func(context.Context, meter.Charged) error { pause(); return nil })
			return err
		},
	},
	"a Refund": {
		setup: func(t *testing.T, f fixture) any {
			c, err := f.chargeOf(f.childSubject(), cent)
			expect.Must(t, err == nil, "charge: %v", err)
			return c.ChargeID
		},
		end: func(f fixture, made any, pause func()) error {
			_, err := f.Usage.Refund(f.Ctx, meter.RefundRequest{ChargeID: made.(uuid.UUID), Remaining: true},
				func(context.Context, meter.Refunded) error { pause(); return nil })
			return err
		},
	},
}

// A period reset that sets a ceiling on a tenant with none, sent while a
// Settle's onCharged or a Refund's onRefunded runs, leaves the tenant
// whole: once both are done, a charge and another reset of the tenant
// succeed. A store that keeps such a tenant's spend apart from its
// aggregate — pgstore's stripes — must not let the call land its change in
// the period the reset closed. The reset may wait for the call, or answer
// ErrTenantBudgetBusy; the callback does not wait for it.
func checkEndingCallsBesideAReset(t *testing.T, f fixture) {
	lift := func(t *testing.T) {
		t.Helper()
		b := f.tenantOf(t)
		_, err := f.Usage.SetTenantBudget(f.Ctx, meter.SetTenantBudgetRequest{TenantID: f.Tenant, Ceiling: unlimitedBudget, ExpectedVersion: b.ResourceVersion})
		expect.Must(t, err == nil, "the tenant's ceiling lifted: %v", err)
	}
	for name, call := range endingCalls {
		t.Run(name, func(t *testing.T) {
			lift(t)
			made := call.setup(t, f)
			// Set again, the ceiling folds what the setup kept apart, so
			// the call starts the tenant's spend anew where a store keeps
			// it apart.
			lift(t)
			version := f.tenantOf(t).ResourceVersion
			reset := make(chan error, 1)
			pause := func() {
				go func() {
					_, err := f.Usage.SetTenantBudget(f.Ctx, meter.SetTenantBudgetRequest{
						TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Max: tenantBudget, UnitCode: unit}, ResetSpend: true, ExpectedVersion: version,
					})
					reset <- err
				}()
				time.Sleep(endingCallbackPause)
			}
			err := call.end(f, made, pause)
			expect.Must(t, err == nil, "%s whose callback sent a reset: %v", name, err)
			err = <-reset
			expect.Must(t, err == nil || errors.Is(err, meter.ErrTenantBudgetBusy), "the reset sent during %s: %v", name, err)
			_, err = f.chargeOf(f.childSubject(), cent)
			expect.That(t, err == nil, "a charge after %s and the reset: %v", name, err)
			b := f.tenantOf(t)
			_, err = f.Usage.SetTenantBudget(f.Ctx, meter.SetTenantBudgetRequest{
				TenantID: f.Tenant, Ceiling: limes.BudgetCeiling{Max: tenantBudget, UnitCode: unit}, ResetSpend: true, ExpectedVersion: b.ResourceVersion,
			})
			expect.That(t, err == nil, "a reset after %s and the reset: %v", name, err)
		})
	}
}
