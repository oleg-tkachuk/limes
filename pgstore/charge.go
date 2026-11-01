package pgstore

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// Bump counts one request against the capability, each ancestor and each
// copy, or none when a ceiling would be crossed.
func (s *UsageStore) Bump(ctx context.Context, req meter.BumpRequest) (meter.BumpReceipt, error) {
	if err := req.Validate(); err != nil {
		return meter.BumpReceipt{}, err
	}
	var receipt meter.BumpReceipt
	err := s.retryDeadlock(ctx, retriedBump, func() error {
		return s.inTenantTx(ctx, req.Subject.TenantID(), func(tx pgx.Tx) error {
			var err error
			receipt.RequestCount, err = s.bump(ctx, tx, req.Subject)
			return err
		})
	})
	if err != nil {
		return meter.BumpReceipt{RequestCount: receipt.RequestCount}, err
	}
	return receipt, nil
}

// bump is Bump in tx: the subject's request count after this request, or,
// refused once its counters are locked, the count before it.
func (s *UsageStore) bump(ctx context.Context, tx pgx.Tx, subject limes.MeterSubject) (int64, error) {
	ancestors, err := readLineage(ctx, tx, subject.TenantID(), subject.CapabilityID())
	if err != nil {
		return 0, err
	}
	c, err := lock(ctx, tx, lockRequest{
		now:     s.clock(),
		created: append([]uuid.UUID{subject.CapabilityID()}, ancestorIDs(ancestors)...), usageTenant: subject.TenantID(),
		copies: copyIDs(subject.Copies()), createCopies: true, copiesCapID: subject.CapabilityID(), maxCopies: subject.CopyQuota().Bounded(s.maxCopies),
		chainQuotas: quotasOf(subject.CapabilityID(), subject.CopyQuota(), ancestors),
	})
	if err != nil {
		return 0, err
	}
	count := c.usage[subject.CapabilityID()].requests
	if err := admitRequest(c, subject, ancestors); err != nil {
		return count, err
	}
	countRequest(c, subject.UnitCode())
	return count + 1, c.write(ctx, tx, s.clock(), false)
}

// admitRequest refuses one more request of subject past a copy's, its own
// or an ancestor's request ceiling.
func admitRequest(c *counters, subject limes.MeterSubject, ancestors []ancestor) error {
	for _, cp := range subject.Copies() {
		if !cp.AdmitsRequests(c.copyOf(cp.RevocationID).requests + 1) {
			return fmt.Errorf("%w: copy %x", limes.ErrRequestLimitExceeded, cp.RevocationID)
		}
	}
	if !subject.Requests().Admits(c.usage[subject.CapabilityID()].requests + 1) {
		return limes.ErrRequestLimitExceeded
	}
	for _, a := range ancestors {
		if !a.requests.Admits(c.usageOf(a.id).requests + 1) {
			return fmt.Errorf("%w: ancestor %s", limes.ErrRequestLimitExceeded, a.id)
		}
	}
	return nil
}

// countRequest counts one more request on every counter in c, and gives a
// usage counter that names no unit unit.
func countRequest(c *counters, unit string) {
	for _, row := range c.usage {
		row.requests++
		row.unit = cmp.Or(row.unit, unit)
	}
	for _, row := range c.copies {
		row.requests++
	}
}

const (
	sqlChargeByRef = `
SELECT id, capability_id, tenant_id, amount, refunded, unit_code, external_ref, overrun, op, actor, at,
       ancestors, copies, reservation_id, tenant_period, lease_id, lease_holder
FROM limes.charges WHERE capability_id = $1 AND external_ref = $2 AND external_ref <> ''`

	sqlChargeByID = `
SELECT id, capability_id, tenant_id, amount, refunded, unit_code, external_ref, overrun, op, actor, at,
       ancestors, copies, reservation_id, tenant_period, lease_id, lease_holder
FROM limes.charges WHERE id = $1`

	sqlChargeByReservation = `
SELECT id, capability_id, tenant_id, amount, refunded, unit_code, external_ref, overrun, op, actor, at,
       ancestors, copies, reservation_id, tenant_period, lease_id, lease_holder
FROM limes.charges WHERE reservation_id = $1`

	sqlInsertCharge = `
INSERT INTO limes.charges
    (id, capability_id, tenant_id, amount, unit_code, op, actor, at, ancestors, copies,
     external_ref, reservation_id, overrun, tenant_period, lease_id, lease_holder)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`

	sqlSpent = `SELECT spent FROM limes.usage WHERE capability_id = $1`
)

// ledgerEntry is one row of the charges ledger.
type ledgerEntry struct {
	meter.ChargeRecord
	ancestors     []uuid.UUID
	copies        [][]byte
	reservationID *uuid.UUID
	tenantPeriod  int64
	// leaseID is the lease the charge was taken under, and leaseHolder its
	// capability; nil for a charge of the whole chain.
	leaseID, leaseHolder *uuid.UUID
}

// readEntry reads the one ledger row query names, if there is one.
func readEntry(ctx context.Context, tx pgx.Tx, query string, args ...any) (ledgerEntry, bool, error) {
	return scanEntry(tx.QueryRow(ctx, query, args...))
}

// scanEntry reads the one ledger row of row, if there is one.
func scanEntry(row pgx.Row) (ledgerEntry, bool, error) {
	var e ledgerEntry
	var amount, refunded int64
	var op string
	err := row.Scan(
		&e.ID, &e.CapabilityID, &e.TenantID, &amount, &refunded, &e.UnitCode, &e.ExternalRef, &e.Overrun,
		&op, &e.Actor, &e.At, &e.ancestors, &e.copies, &e.reservationID, &e.tenantPeriod, &e.leaseID, &e.leaseHolder)
	if errors.Is(err, pgx.ErrNoRows) {
		return ledgerEntry{}, false, nil
	}
	if err != nil {
		return ledgerEntry{}, false, err
	}
	e.Amount, e.RefundedAmount, e.Op = limes.Nanos(amount), limes.Nanos(refunded), limes.Op(op)
	return e, true, nil
}

// replay is the receipt of a charge already in the ledger, returned when the
// same charge arrives again.
func replay(ctx context.Context, tx pgx.Tx, e ledgerEntry) (meter.ChargeReceipt, error) {
	var spent int64
	err := tx.QueryRow(ctx, sqlSpent, e.CapabilityID).Scan(&spent)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return meter.ChargeReceipt{}, err
	}
	return meter.ChargeReceipt{
		ChargeID: e.ID, SpentAmount: limes.Nanos(spent), Replayed: true, Overrun: e.Overrun,
		TenantID: e.TenantID, UnitCode: e.UnitCode,
	}, nil
}

// receiptOfRef answers a charge whose (capability, ExternalRef) is already
// in the ledger as e: its receipt again, or ErrExternalRefConflict when e
// names another cost.
func receiptOfRef(ctx context.Context, tx pgx.Tx, req meter.ChargeRequest, unit string, e ledgerEntry) (meter.ChargeReceipt, bool, error) {
	// Another copy charges its own counters, and the receipt is the first
	// copy's, so it conflicts as another cost does. The error names the ref,
	// never the other charge.
	if e.Amount != req.Amount || e.UnitCode != unit || e.TenantID != req.Subject.TenantID() || !sameCopyIDs(e.copies, req.Subject.Copies()) {
		return meter.ChargeReceipt{}, true, fmt.Errorf("%w: %q", meter.ErrExternalRefConflict, req.ExternalRef)
	}
	r, err := replay(ctx, tx, e)
	return r, true, err
}

// Charge applies the ceiling chain and commits the charge, onCharged
// included, in one transaction.
func (s *UsageStore) Charge(ctx context.Context, req meter.ChargeRequest, onCharged func(ctx context.Context, c meter.Charged) error) (meter.ChargeReceipt, error) {
	if err := req.Validate(); err != nil {
		return meter.ChargeReceipt{}, err
	}
	var receipt meter.ChargeReceipt
	attempt := func() error {
		return s.retryDeadlock(ctx, retriedCharge, func() error {
			return s.inTenantTx(ctx, req.Subject.TenantID(), func(tx pgx.Tx) error {
				var err error
				receipt, err = s.charge(ctx, tx, req, onCharged)
				return err
			})
		})
	}
	err := attempt()
	// A lease that moved between its read and its lock is read afresh.
	if errors.Is(err, errLeaseMoved) {
		err = attempt()
	}
	if errors.Is(err, errReplayedAfterLock) {
		return receipt, nil
	}
	return receipt, err
}

// charge is Charge in tx.
func (s *UsageStore) charge(ctx context.Context, tx pgx.Tx, req meter.ChargeRequest, onCharged func(ctx context.Context, c meter.Charged) error) (meter.ChargeReceipt, error) {
	unit := req.Subject.UnitCode()
	// A replay is answered before the lineage is read: it moves nothing,
	// so it needs no record, and a capability purged since still gets its
	// receipt, as a settled hold does.
	p, err := readPrelude(ctx, tx, req.Subject.TenantID(), req.Subject.CapabilityID(), req.ExternalRef, leasable(req))
	if err != nil {
		return meter.ChargeReceipt{}, err
	}
	if p.replayed {
		r, _, err := receiptOfRef(ctx, tx, req, unit, p.replay)
		return r, err
	}
	ancestors, err := p.ancestors, p.lineageErr
	if err != nil {
		return meter.ChargeReceipt{}, err
	}
	// Under a lease of its own or an ancestor's, the charge stops at the
	// leaseholder (docs/adr/0017-budget-leases.md).
	if holder, below, ok := leaseFor(req.Subject.CapabilityID(), ancestors, p.leases, s.clock()); ok {
		return s.chargeLeased(ctx, tx, req, unit, ancestors, below, holder, p.leases[holder], onCharged)
	}
	spec := chargeSpec{
		capID: req.Subject.CapabilityID(), tenantID: req.Subject.TenantID(), budget: req.Subject.Budget(),
		unit: unit, copies: req.Subject.Copies(), copyQuota: req.Subject.CopyQuota(), amount: req.Amount, op: req.Op, actor: req.Actor,
		externalRef: req.ExternalRef, overrun: req.Overrun, ancestors: ancestors,
		replayAfterLock: req.ExternalRef != "",
	}
	c, err := s.lockCharge(ctx, tx, spec)
	if err != nil {
		return meter.ChargeReceipt{}, err
	}
	// Another delivery of the same reference may have committed while this
	// one waited for the locks. Its receipt is the answer, and what locking
	// made here — counters, a copy recorded again after its counters were
	// dropped — is rolled back: a replay moves nothing.
	if c.replayed {
		r, _, err := receiptOfRef(ctx, tx, req, unit, c.replay)
		return r, cmp.Or(err, errReplayedAfterLock)
	}
	return s.commitCharge(ctx, tx, c, spec, onCharged)
}

// errReplayedAfterLock rolls back a Charge that found its reference
// committed by another delivery once it held the locks; Charge answers
// with that delivery's receipt.
var errReplayedAfterLock = fmt.Errorf("%w: pgstore: charge replayed after its locks", limes.ErrInternal)

// lockCharge locks what spec moves: its tenant, the capability's and its
// ancestors' counters, those of the ancestors holding the hold it settles,
// and its copies'.
func (s *UsageStore) lockCharge(ctx context.Context, tx pgx.Tx, spec chargeSpec) (*counters, error) {
	l := lockRequest{
		now:      s.clock(),
		tenantID: spec.tenantID, tenantUnit: spec.unit,
		created:     append([]uuid.UUID{spec.capID}, ancestorIDs(spec.ancestors)...),
		usageTenant: spec.tenantID,
		copies:      copyIDs(spec.copies), createCopies: true, copiesCapID: spec.capID, maxCopies: spec.copyQuota.Bounded(s.maxCopies),
		chainQuotas: quotasOf(spec.capID, spec.copyQuota, spec.ancestors),
	}
	if spec.settling != nil {
		l.existing = spec.settling.ancestors
	}
	if spec.replayAfterLock {
		l.replayCapID, l.replayRef = spec.capID, spec.externalRef
	}
	return lock(ctx, tx, l)
}

// chargeSpec is one charge to commit, in plain values: a Charge's request, or
// a Settle's hold beside the settling subject's own ceiling.
type chargeSpec struct {
	capID, tenantID uuid.UUID
	// budget is the capability's own ceiling, from its token.
	budget limes.BudgetCeiling
	// unit is normalised.
	unit   string
	copies []limes.CopyCeiling
	// copyQuota is the capability's quota of copies, from its token.
	copyQuota   limes.CopyQuota
	amount      limes.Nanos
	op          limes.Op
	actor       string
	externalRef string
	overrun     meter.OverrunPolicy
	// ancestors are the lineage the charge spends on, as it stands.
	ancestors []ancestor
	// settling is the hold the charge settles; nil for a direct charge.
	settling *hold
	// replayAfterLock reads the charge on record under externalRef once
	// the locks are held (lockRequest.replayRef).
	replayAfterLock bool
}

// commitCharge checks every ceiling against c and, when all admit the
// charge, runs onCharged and writes it, with the release of the hold it
// settles. The charge is spent on the lineage as it stands; the hold is
// released from the ancestors that hold it, which a row dropped since may
// have left.
func (s *UsageStore) commitCharge(ctx context.Context, tx pgx.Tx, c *counters, spec chargeSpec, onCharged func(context.Context, meter.Charged) error) (meter.ChargeReceipt, error) {
	var released limes.Nanos
	var holders []uuid.UUID
	var reservationID *uuid.UUID
	if spec.settling != nil {
		released, holders, reservationID = spec.settling.amount, spec.settling.ancestors, &spec.settling.id
	}
	refused := meter.ChargeReceipt{SpentAmount: c.usageOf(spec.capID).spent, TenantID: spec.tenantID, UnitCode: spec.unit}
	overrun := false
	crossed := func(err error) error {
		if spec.overrun == meter.OverrunPolicyRecord {
			overrun = true
			return nil
		}
		return err
	}
	if err := checkCeilings(c, ceilingCheck{
		amount: spec.amount, released: released, holders: holders, budget: spec.budget, unit: spec.unit,
		capID: spec.capID, ancestors: spec.ancestors, copies: spec.copies,
	}, crossed); err != nil {
		return refused, err
	}
	receipt := meter.ChargeReceipt{
		ChargeID: uuid.New(), SpentAmount: c.usageOf(spec.capID).spent + spec.amount, Overrun: overrun,
		TenantID: spec.tenantID, UnitCode: spec.unit,
	}
	if err := runOnCharged(ctx, tx, receipt, onCharged); err != nil {
		return refused, err
	}
	spend(c, spec, holders, released)
	now := s.clock()
	insert := heldStmt{sql: sqlInsertCharge, args: []any{
		receipt.ChargeID, spec.capID, spec.tenantID, int64(spec.amount), spec.unit, string(spec.op), spec.actor, now,
		ancestorIDs(spec.ancestors), copyIDs(spec.copies), spec.externalRef, reservationID, overrun, c.tenant.period, nil, nil,
	}}
	if err := c.write(ctx, tx, now, true, insert); err != nil {
		return refused, err
	}
	return receipt, nil
}

// runOnCharged runs the caller's onCharged, if any, in tx, and refuses a
// callback that ended tx.
func runOnCharged(ctx context.Context, tx pgx.Tx, receipt meter.ChargeReceipt, onCharged func(context.Context, meter.Charged) error) error {
	if onCharged == nil {
		return nil
	}
	xact, err := currentXact(ctx, tx)
	if err != nil {
		return err
	}
	callbackCtx, end := withTx(ctx, tx)
	err = func() error {
		defer end()
		return onCharged(callbackCtx, meter.Charged{Receipt: receipt})
	}()
	if err != nil {
		return err
	}
	return stillOpen(ctx, tx, xact)
}

// spend adds spec's amount to every counter in c it is spent on — the
// capability, its ancestors, its tenant and its copies — releasing what
// the holders held of it.
func spend(c *counters, spec chargeSpec, holders []uuid.UUID, released limes.Nanos) {
	u := c.usage[spec.capID]
	u.spent += spec.amount
	u.reserved = max0(u.reserved - released)
	u.unit = spec.unit
	for _, anc := range spec.ancestors {
		au := c.usage[anc.id]
		au.spent += spec.amount
		au.unit = cmp.Or(au.unit, spec.unit)
	}
	for _, id := range holders {
		if au, ok := c.usage[id]; ok {
			au.reserved = max0(au.reserved - released)
		}
	}
	c.tenant.spent += spec.amount
	c.tenant.reserved = max0(c.tenant.reserved - released)
	c.tenant.unit = cmp.Or(c.tenant.unit, spec.unit)
	for _, cp := range spec.copies {
		r := c.copies[string(cp.RevocationID)]
		r.spent += spec.amount
		r.reserved = max0(r.reserved - released)
	}
}

// ceilingCheck is an amount measured against every ceiling. released is the
// amount of a hold being settled by it, which no longer counts.
type ceilingCheck struct {
	amount, released limes.Nanos
	// holders are the ancestors that hold released; the capability, its
	// copies and its tenant always do.
	holders   []uuid.UUID
	budget    limes.BudgetCeiling // the capability's own, from its token
	unit      string              // normalised
	capID     uuid.UUID
	ancestors []ancestor
	copies    []limes.CopyCeiling
}

// checkCeilings measures k against the copies' ceilings innermost first, then
// the capability's, each ancestor's and the tenant aggregate's, with what is
// already spent and held. Each ceiling k would cross goes to crossed, which
// returns the error to stop on or nil to carry on.
func checkCeilings(c *counters, k ceilingCheck, crossed func(error) error) error {
	if err := checkUnits(c, k); err != nil {
		return err
	}
	if err := checkRange(c, k); err != nil {
		return err
	}
	if err := checkCopyCeilings(c, k, crossed); err != nil {
		return err
	}
	if err := checkCapabilityCeiling(c, k, crossed); err != nil {
		return err
	}
	if err := checkAncestorCeilings(c, k, crossed); err != nil {
		return err
	}
	return checkTenantCeiling(c, k, crossed)
}

// withinHold reports whether k settles at or below its hold, which adds
// nothing to a counter that holds it, so no ceiling there can refuse it —
// not even one crossed since the hold was made. Only the excess must fit.
// An ancestor that does not hold it takes the whole amount.
func (k ceilingCheck) withinHold() bool { return k.amount <= k.released }

// checkCopyCeilings measures k against each copy's budget, innermost first.
func checkCopyCeilings(c *counters, k ceilingCheck, crossed func(error) error) error {
	if k.withinHold() {
		return nil
	}
	for _, cp := range k.copies {
		r := c.copyOf(cp.RevocationID)
		if !cp.AdmitsBudget(r.spent + r.reserved - k.released + k.amount) {
			if err := crossed(fmt.Errorf("%w: copy %x", limes.ErrBudgetExceeded, cp.RevocationID)); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkCapabilityCeiling measures k against the capability's own budget.
func checkCapabilityCeiling(c *counters, k ceilingCheck, crossed func(error) error) error {
	u := c.usageOf(k.capID)
	if k.withinHold() || k.budget.Admits(u.spent+u.reserved-k.released+k.amount) {
		return nil
	}
	return crossed(limes.ErrBudgetExceeded)
}

// checkAncestorCeilings measures k against each ancestor's budget, less
// what that ancestor holds of it.
func checkAncestorCeilings(c *counters, k ceilingCheck, crossed func(error) error) error {
	for _, a := range k.ancestors {
		au := c.usageOf(a.id)
		held := limes.Nanos(0)
		if slices.Contains(k.holders, a.id) {
			held = k.released
		}
		if k.amount <= held {
			continue
		}
		if !a.budget.Admits(au.spent + au.reserved - held + k.amount) {
			if err := crossed(fmt.Errorf("%w: ancestor %s", limes.ErrBudgetExceeded, a.id)); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkTenantCeiling measures k against its tenant's aggregate budget.
func checkTenantCeiling(c *counters, k ceilingCheck, crossed func(error) error) error {
	b := c.tenant
	if k.withinHold() || b.ceiling.Admits(b.spent+k.amount+max0(b.reserved-k.released)) {
		return nil
	}
	return crossed(limes.ErrTenantBudgetExceeded)
}

// checkUnits refuses an amount in a unit other than one a counter it would
// move already counts in. Like the range, it is not a ceiling an overrun may
// cross: the sum would mean nothing.
func checkUnits(c *counters, k ceilingCheck) error {
	differs := func(have string) bool { return have != "" && have != k.unit }
	if have := c.usageOf(k.capID).unit; differs(have) {
		return fmt.Errorf("%w: capability %s counts in %s, not %s", limes.ErrUnitCodeMismatch, k.capID, have, k.unit)
	}
	for _, a := range k.ancestors {
		if have := c.usageOf(a.id).unit; differs(have) {
			return fmt.Errorf("%w: ancestor %s counts in %s, not %s", limes.ErrUnitCodeMismatch, a.id, have, k.unit)
		}
	}
	if differs(c.tenant.unit) {
		return fmt.Errorf("%w: tenant %s counts in %s, not %s", limes.ErrUnitCodeMismatch, c.tenant.tenantID, c.tenant.unit, k.unit)
	}
	return nil
}

// checkRange keeps every counter k would move within MaxNanos, the range a
// store holds. Unlike a ceiling it is not an overrun to record: past it the
// amount could not be counted at all.
func checkRange(c *counters, k ceilingCheck) error {
	fits := func(spent, reserved limes.Nanos) bool {
		return spent+max0(reserved-k.released) <= limes.MaxNanos-k.amount
	}
	ok := true
	for _, cp := range k.copies {
		r := c.copyOf(cp.RevocationID)
		ok = ok && fits(r.spent, r.reserved)
	}
	u := c.usageOf(k.capID)
	ok = ok && fits(u.spent, u.reserved)
	for _, a := range k.ancestors {
		au := c.usageOf(a.id)
		ok = ok && fits(au.spent, au.reserved)
	}
	ok = ok && fits(c.tenant.spent, c.tenant.reserved)
	if !ok {
		return fmt.Errorf("%w: %s more", meter.ErrCounterOverflow, k.amount)
	}
	return nil
}

// hold is one open reservation, as the reservations table keeps it.
type hold struct {
	id        uuid.UUID
	capID     uuid.UUID
	tenantID  uuid.UUID
	ancestors []uuid.UUID
	amount    limes.Nanos
	unit      string
	op        limes.Op
	actor     string
	copies    []limes.CopyCeiling
	expires   time.Time
}

func (h hold) reservation() meter.Reservation {
	r := meter.Reservation{
		ID: h.id, CapabilityID: h.capID, TenantID: h.tenantID, Amount: h.amount, UnitCode: h.unit,
		Op: h.op, Actor: h.actor, ExpiresAt: h.expires,
	}
	for _, c := range h.copies {
		r.Copies = append(r.Copies, meter.HeldCopy{RevocationID: c.RevocationID, Budget: c.Budget})
	}
	return r
}

const (
	sqlHoldColumns = `id, capability_id, tenant_id, ancestors, amount, unit_code, op, actor, copies, expires_at`

	sqlInsertHold = `
INSERT INTO limes.reservations (` + sqlHoldColumns + `)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

	sqlLockHold = `SELECT ` + sqlHoldColumns + ` FROM limes.reservations WHERE id = $1 FOR UPDATE`

	sqlGetHold = `SELECT ` + sqlHoldColumns + ` FROM limes.reservations WHERE id = $1`

	sqlDeleteHold = `DELETE FROM limes.reservations WHERE id = $1`
)

// scanHold reads one row of sqlHoldColumns.
func scanHold(row pgx.Row) (hold, error) {
	var h hold
	var amount int64
	var op string
	var copies []byte
	if err := row.Scan(&h.id, &h.capID, &h.tenantID, &h.ancestors, &amount, &h.unit, &op, &h.actor, &copies, &h.expires); err != nil {
		return hold{}, err
	}
	h.amount, h.op = limes.Nanos(amount), limes.Op(op)
	var err error
	h.copies, err = decodeHeldCopies(copies, h.unit)
	return h, err
}

// Reserve holds an amount against every ceiling Charge checks.
func (s *UsageStore) Reserve(ctx context.Context, req meter.ReserveRequest) (meter.Reservation, error) {
	if err := req.Validate(); err != nil {
		return meter.Reservation{}, err
	}
	copies, err := encodeHeldCopies(req.Subject.Copies())
	if err != nil {
		return meter.Reservation{}, err
	}
	var out meter.Reservation
	err = s.retryDeadlock(ctx, retriedReserve, func() error {
		return s.inTenantTx(ctx, req.Subject.TenantID(), func(tx pgx.Tx) error {
			var err error
			out, err = s.reserve(ctx, tx, req, copies)
			return err
		})
	})
	return out, err
}

// reserve is Reserve in tx, copies the encoded copies the hold is under.
func (s *UsageStore) reserve(ctx context.Context, tx pgx.Tx, req meter.ReserveRequest, copies []byte) (meter.Reservation, error) {
	subject, unit := req.Subject, req.Subject.UnitCode()
	ancestors, err := readLineage(ctx, tx, subject.TenantID(), subject.CapabilityID())
	if err != nil {
		return meter.Reservation{}, err
	}
	c, err := s.lockCharge(ctx, tx, chargeSpec{
		capID: subject.CapabilityID(), tenantID: subject.TenantID(), unit: unit, copies: subject.Copies(), copyQuota: subject.CopyQuota(), ancestors: ancestors,
	})
	if err != nil {
		return meter.Reservation{}, err
	}
	refuse := func(err error) error { return err }
	if err := checkCeilings(c, ceilingCheck{
		amount: req.Amount, budget: subject.Budget(), unit: unit,
		capID: subject.CapabilityID(), ancestors: ancestors, copies: subject.Copies(),
	}, refuse); err != nil {
		return meter.Reservation{}, err
	}
	now := s.clock()
	expires, err := holdExpiry(ctx, tx, subject.CapabilityID(), now, cmp.Or(req.TTL, meter.DefaultReservationTTL))
	if err != nil {
		return meter.Reservation{}, err
	}
	h := hold{
		id: uuid.New(), capID: subject.CapabilityID(), tenantID: subject.TenantID(), ancestors: ancestorIDs(ancestors),
		amount: req.Amount, unit: unit, op: req.Op, actor: req.Actor, copies: subject.Copies(),
		expires: expires.Truncate(timeResolution),
	}
	applyHold(c, h, +1)
	insert := heldStmt{sql: sqlInsertHold, args: []any{
		h.id, h.capID, h.tenantID, h.ancestors, int64(h.amount), h.unit, string(h.op), h.actor, copies, h.expires,
	}}
	if err := c.write(ctx, tx, now, true, insert); err != nil {
		return meter.Reservation{}, err
	}
	return h.reservation(), nil
}

// holdExpiry is when a hold of capID made at now for ttl ends: with its
// capability at the latest, since past it no one can settle the hold, and
// it would keep the ancestors' budget held. A capability already expired
// could hold nothing anyone may settle.
func holdExpiry(ctx context.Context, tx pgx.Tx, capID uuid.UUID, now time.Time, ttl time.Duration) (time.Time, error) {
	expires := now.Add(ttl)
	var capExpires *time.Time
	if err := tx.QueryRow(ctx, sqlCapabilityExpiry, capID).Scan(&capExpires); err != nil {
		return time.Time{}, err
	}
	if capExpires == nil {
		return expires, nil
	}
	if !capExpires.After(now) {
		return time.Time{}, limes.ErrExpired
	}
	if capExpires.Before(expires) {
		return *capExpires, nil
	}
	return expires, nil
}

// applyHold adds (sign +1) or removes (sign -1) h on every counter c holds
// of it.
func applyHold(c *counters, h hold, sign limes.Nanos) {
	for _, id := range append([]uuid.UUID{h.capID}, h.ancestors...) {
		if u, ok := c.usage[id]; ok {
			u.reserved = max0(u.reserved + sign*h.amount)
			if u.unit == "" {
				u.unit = h.unit
			}
		}
	}
	c.tenant.reserved = max0(c.tenant.reserved + sign*h.amount)
	if c.tenant.unit == "" {
		c.tenant.unit = h.unit
	}
	for _, cp := range h.copies {
		if r, ok := c.copies[string(cp.RevocationID)]; ok {
			r.reserved = max0(r.reserved + sign*h.amount)
		}
	}
}

// Settle charges the actual cost in place of a reservation.
func (s *UsageStore) Settle(ctx context.Context, req meter.SettleRequest, onCharged func(ctx context.Context, c meter.Charged) error) (meter.ChargeReceipt, error) {
	if err := req.Validate(); err != nil {
		return meter.ChargeReceipt{}, err
	}
	// A direct charge has no reservation id, so the zero id names none.
	if req.ReservationID == uuid.Nil() {
		return meter.ChargeReceipt{}, meter.ErrReservationNotFound
	}
	var receipt meter.ChargeReceipt
	err := s.retryDeadlock(ctx, retriedSettle, func() error {
		return s.inTenantTx(ctx, req.Subject.TenantID(), func(tx pgx.Tx) error {
			h, err := scanHold(tx.QueryRow(ctx, sqlLockHold, req.ReservationID))
			if errors.Is(err, pgx.ErrNoRows) {
				receipt, err = settled(ctx, tx, req)
				return err
			}
			if err != nil {
				return err
			}
			if !s.holdServes(h, req.Subject) {
				return meter.ErrReservationNotFound
			}
			receipt, err = s.settleHold(ctx, tx, req, h, onCharged)
			return err
		})
	})
	return receipt, err
}

// settled is the receipt of req's reservation, settled already by the same
// subject; a settle of another amount conflicts. A reservation of another
// capability or tenant, or settled under other copies, is none of this
// one's: its receipt is not this subject's to read back either.
func settled(ctx context.Context, tx pgx.Tx, req meter.SettleRequest) (meter.ChargeReceipt, error) {
	subject := req.Subject
	e, found, err := readEntry(ctx, tx, sqlChargeByReservation, req.ReservationID)
	if err != nil {
		return meter.ChargeReceipt{}, err
	}
	if !found || e.CapabilityID != subject.CapabilityID() || e.TenantID != subject.TenantID() || !sameCopyIDs(e.copies, subject.Copies()) {
		return meter.ChargeReceipt{}, meter.ErrReservationNotFound
	}
	if e.Amount != req.Amount {
		return meter.ChargeReceipt{}, fmt.Errorf("%w: %s", meter.ErrSettleConflict, req.ReservationID)
	}
	return replay(ctx, tx, e)
}

// holdServes reports whether h is a live hold of subject's, under the
// copies subject presents.
func (s *UsageStore) holdServes(h hold, subject limes.MeterSubject) bool {
	return h.capID == subject.CapabilityID() && h.tenantID == subject.TenantID() &&
		h.expires.After(s.clock()) && sameCopyIDs(copyIDs(h.copies), subject.Copies())
}

// settleHold charges req against the hold h it settles, and deletes the
// hold. The charge is the reservation's: its tenant, unit and the copy
// limits it was made under, against the subject's own ceiling.
func (s *UsageStore) settleHold(ctx context.Context, tx pgx.Tx, req meter.SettleRequest, h hold, onCharged func(ctx context.Context, c meter.Charged) error) (meter.ChargeReceipt, error) {
	ancestors, err := lineage(ctx, tx, h.capID)
	if err != nil {
		return meter.ChargeReceipt{}, err
	}
	spec := chargeSpec{
		capID: h.capID, tenantID: h.tenantID, budget: req.Subject.Budget(), unit: h.unit, copies: h.copies, copyQuota: req.Subject.CopyQuota(),
		amount: req.Amount, op: h.op, actor: h.actor, overrun: req.Overrun, ancestors: ancestors, settling: &h,
	}
	c, err := s.lockCharge(ctx, tx, spec)
	if err != nil {
		return meter.ChargeReceipt{}, err
	}
	receipt, err := s.commitCharge(ctx, tx, c, spec, onCharged)
	if err != nil {
		return receipt, err
	}
	_, err = tx.Exec(ctx, sqlDeleteHold, h.id)
	return receipt, err
}

// Release ends a reservation without charging. Releasing one that is gone
// is a no-op, and so is releasing one of another capability or tenant, or
// of other copies: it is left held.
func (s *UsageStore) Release(ctx context.Context, req meter.ReleaseRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	subject := req.Subject
	return s.retryDeadlock(ctx, retriedRelease, func() error {
		return s.inTenantTx(ctx, subject.TenantID(), func(tx pgx.Tx) error {
			h, err := scanHold(tx.QueryRow(ctx, sqlLockHold, req.ReservationID))
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			if h.capID != subject.CapabilityID() || h.tenantID != subject.TenantID() || !sameCopyIDs(copyIDs(h.copies), subject.Copies()) {
				return nil
			}
			return s.releaseHold(ctx, tx, h)
		})
	})
}

// releaseHold returns h to every counter that still holds it and deletes
// it; the hold's row is locked.
func (s *UsageStore) releaseHold(ctx context.Context, tx pgx.Tx, h hold) error {
	c, err := lock(ctx, tx, lockRequest{
		now:      s.clock(),
		tenantID: h.tenantID,
		existing: append([]uuid.UUID{h.capID}, h.ancestors...),
		copies:   copyIDs(h.copies), copiesCapID: h.capID,
	})
	if err != nil {
		return err
	}
	applyHold(c, h, -1)
	return c.write(ctx, tx, s.clock(), c.tenant.persisted, heldStmt{sql: sqlDeleteHold, args: []any{h.id}})
}
