package pgstore

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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
	attempt := func() error {
		return s.retryDeadlock(ctx, retriedBump, func() error {
			return s.inTenantTx(ctx, req.Subject.TenantID(), func(tx pgx.Tx) error {
				var err error
				receipt.RequestCount, err = s.bump(ctx, tx, req.Subject)
				return err
			})
		})
	}
	// A lease that moved between its read and its lock is read afresh.
	err := s.retryLeaseMoved(ctx, retriedBump, req.Subject.CapabilityID(), attempt)
	if err != nil {
		return meter.BumpReceipt{RequestCount: receipt.RequestCount}, err
	}
	return receipt, nil
}

// bump is Bump in tx: the subject's request count after this request, or,
// refused once its counters are locked, the count before it.
func (s *UsageStore) bump(ctx context.Context, tx pgx.Tx, subject limes.MeterSubject) (int64, error) {
	// A bump of a Biscuit copy, whose quota counts on every ancestor's row,
	// goes through the whole chain, as a charge of one does.
	p, err := readPrelude(ctx, tx, subject.TenantID(), subject.CapabilityID(), "", len(subject.Copies()) == 0)
	if err != nil {
		return 0, err
	}
	ancestors, err := p.ancestors, p.lineageErr
	if err != nil {
		return 0, err
	}
	// Under a lease of its own or an ancestor's that holds requests, the
	// bump stops at the leaseholder (docs/adr/0017-budget-leases.md).
	if holder, below, ok := leaseFor(subject.CapabilityID(), ancestors, p.leases, s.asOf(p.now), leaseState.coversBumps); ok {
		return s.bumpLeased(ctx, tx, subject, ancestors, below, holder, p.leases[holder], s.asOf(p.now))
	}
	c, err := s.lock(ctx, tx, lockRequest{
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
	return count + 1, c.writeLast(ctx, tx, s.clock(), false)
}

// admitRequest refuses one more request of subject past a copy's, its own
// or an ancestor's request ceiling.
func admitRequest(c *counters, subject limes.MeterSubject, ancestors []ancestor) error {
	for _, cp := range subject.Copies() {
		if !cp.AdmitsRequests(c.copyOf(cp.RevocationID).requests + 1) {
			return fmt.Errorf("%w: copy %x", limes.ErrRequestLimitExceeded, cp.RevocationID)
		}
	}
	return admitRequests(c, subject, ancestors, 1)
}

// admitRequests refuses n more requests of subject past its own or an
// ancestor's request ceiling, the requests leases hold of each counted as
// made.
func admitRequests(c *counters, subject limes.MeterSubject, ancestors []ancestor, n int64) error {
	used := func(id uuid.UUID) int64 { u := c.usageOf(id); return u.requests + u.reservedRequests }
	if !subject.Requests().AdmitsMore(used(subject.CapabilityID()), n) {
		return limes.ErrRequestLimitExceeded
	}
	for _, a := range ancestors {
		if !a.requests.AdmitsMore(used(a.id), n) {
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
       ancestors, copies, reservation_id, tenant_period, lease_id, lease_holder, seq
FROM limes.charges WHERE capability_id = $1 AND external_ref = $2 AND external_ref <> ''`

	sqlChargeByID = `
SELECT id, capability_id, tenant_id, amount, refunded, unit_code, external_ref, overrun, op, actor, at,
       ancestors, copies, reservation_id, tenant_period, lease_id, lease_holder, seq
FROM limes.charges WHERE id = $1`

	sqlChargeByReservation = `
SELECT id, capability_id, tenant_id, amount, refunded, unit_code, external_ref, overrun, op, actor, at,
       ancestors, copies, reservation_id, tenant_period, lease_id, lease_holder, seq
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
	// seq numbers the charge among the counter rows (migration 037): a row
	// born after it never counted it.
	seq int64
}

// counted reports whether a counter row born at born counted e.
func (e ledgerEntry) counted(born int64) bool {
	return born < e.seq
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
		&op, &e.Actor, &e.At, &e.ancestors, &e.copies, &e.reservationID, &e.tenantPeriod, &e.leaseID, &e.leaseHolder, &e.seq)
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
	// A lease that moved between its read and its lock is read afresh.
	err := s.retryLeaseMoved(ctx, retriedCharge, req.Subject.CapabilityID(), attempt)
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
	if holder, below, ok := leaseFor(req.Subject.CapabilityID(), ancestors, p.leases, s.asOf(p.now), leaseState.coversCharges); ok {
		return s.chargeLeased(ctx, tx, req, unit, ancestors, below, holder, p.leases[holder], s.asOf(p.now), onCharged)
	}
	spec := chargeSpec{
		capID: req.Subject.CapabilityID(), tenantID: req.Subject.TenantID(), budget: req.Subject.Budget(),
		unit: unit, copies: req.Subject.Copies(), copyQuota: req.Subject.CopyQuota(), amount: req.Amount, op: req.Op, actor: req.Actor,
		externalRef: req.ExternalRef, overrun: req.Overrun, ancestors: ancestors,
		replayAfterLock: req.ExternalRef != "", call: meter.StoreOperationCharge,
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
		now: s.clock(), op: spec.call,
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
	l.holdID = spec.holdID
	return s.lock(ctx, tx, l)
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
	// holdID is the id a keyed Reserve holds under, looked for once the
	// locks are held (lockRequest.holdID); nil for none.
	holdID uuid.UUID
	// call is the store call the charge is made by.
	call meter.StoreOperation
}

// commitCharge checks every ceiling against c and, when all admit the
// charge, writes it, with the release of the hold it settles, and runs
// onCharged after the writes (writeCharged). The charge is spent on the
// lineage as it stands; the hold is released from the ancestors that hold
// it, which a row dropped since may have left, and the hold deleted in the
// same batch. The write is the call's last statement (writeLast).
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
	spend(c, spec, holders, released)
	now := s.clock()
	insert := heldStmt{sql: sqlInsertCharge, args: []any{
		receipt.ChargeID, spec.capID, spec.tenantID, int64(spec.amount), spec.unit, string(spec.op), spec.actor, now,
		ancestorIDs(spec.ancestors), copyIDs(spec.copies), spec.externalRef, reservationID, overrun, c.tenant.period, nil, nil,
	}}
	writes := []heldStmt{insert}
	if spec.settling != nil {
		writes = append(writes, heldStmt{sql: sqlDeleteHold, args: []any{spec.settling.id}})
	}
	if err := c.writeCharged(ctx, tx, now, true, true, receipt, onCharged, writes...); err != nil {
		return refused, err
	}
	return receipt, nil
}

// writeCharged writes c and then, the charge of receipt, and runs the
// caller's onCharged, if any, once they are sent (writeThenCall), so that
// what onCharged writes — an outbox row with a foreign key to the ledger —
// sees the charge's row.
func (c *counters) writeCharged(ctx context.Context, tx pgx.Tx, now time.Time, writeTenant, last bool, receipt meter.ChargeReceipt, onCharged func(context.Context, meter.Charged) error, then ...heldStmt) error {
	var call callback
	if onCharged != nil {
		call = func(ctx context.Context) error { return onCharged(ctx, meter.Charged{Receipt: receipt}) }
	}
	return c.writeThenCall(ctx, tx, now, writeTenant, last, receipt.ChargeID.String(), call, then)
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

	// sqlSettledBy reports whether a charge settled the hold $1.
	sqlSettledBy = `SELECT EXISTS (SELECT 1 FROM limes.charges WHERE reservation_id = $1)`

	// sqlHoldTaken reports whether $1 names a hold on record, or the charge
	// that settled one: what a keyed Reserve reads once it holds its locks.
	sqlHoldTaken = `
SELECT EXISTS (SELECT 1 FROM limes.reservations WHERE id = $1)
    OR EXISTS (SELECT 1 FROM limes.charges WHERE reservation_id = $1)`

	// constraintHoldKey is limes.reservations' primary key, which a hold
	// inserted under the id of another tenant's hold, hidden from the call
	// by row-level security, violates.
	constraintHoldKey = "reservations_pkey"
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
	attempt := func() error {
		return s.retryDeadlock(ctx, retriedReserve, func() error {
			return s.inTenantTx(ctx, req.Subject.TenantID(), func(tx pgx.Tx) error {
				var err error
				out, err = s.reserve(ctx, tx, req, copies)
				return err
			})
		})
	}
	// A hold under the id committed while this call waited for its locks:
	// it is answered afresh, as a replay or a conflict.
	err = s.retryLostRace(ctx, lostRace{
		op: retriedReserve, lost: errHoldTakenAfterLock, gaveUp: errHoldKeptTaken,
		about: []any{
			slog.String(logKeyCapability, req.Subject.CapabilityID().String()),
			slog.String(logKeyReservation, req.ReservationID.String()),
		},
	}, attempt, noneBetween)
	if err != nil {
		return meter.Reservation{}, err
	}
	return out, nil
}

// errHoldTakenAfterLock rolls back a keyed Reserve that found its id taken
// by another call once it held the locks; Reserve runs again, and answers
// from what that call committed — or, losing again, is refused as
// errHoldKeptTaken (retryLostRace).
var errHoldTakenAfterLock = fmt.Errorf("%w: pgstore: reservation id taken while the call waited for its locks", limes.ErrInternal)

// reserved answers a Reserve whose ReservationID is on record already, as a
// hold or as the charge that settled one: keyed reports that it is, with
// the hold again when it was made for req, in unit, and ErrReserveConflict
// when it was made for another request or has been settled. The hold is
// locked first, as Settle and Release lock it, so one settling meanwhile
// is read once it has.
func reserved(ctx context.Context, tx pgx.Tx, req meter.ReserveRequest, unit string) (meter.Reservation, bool, error) {
	b := &pgx.Batch{}
	b.Queue(sqlLockHold, req.ReservationID)
	b.Queue(sqlSettledBy, req.ReservationID)
	br := tx.SendBatch(ctx, b)
	r, keyed, err := func() (meter.Reservation, bool, error) {
		h, err := scanHold(br.QueryRow())
		found := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return meter.Reservation{}, false, err
		}
		var settled bool
		if err := br.QueryRow().Scan(&settled); err != nil {
			return meter.Reservation{}, false, err
		}
		switch {
		case settled:
			return meter.Reservation{}, true, fmt.Errorf("%w: %s is settled", meter.ErrReserveConflict, req.ReservationID)
		case !found:
			return meter.Reservation{}, false, nil
		case !h.madeFor(req, unit):
			return meter.Reservation{}, true, fmt.Errorf("%w: %s", meter.ErrReserveConflict, req.ReservationID)
		}
		out := h.reservation()
		out.Replayed = true
		return out, true, nil
	}()
	return r, keyed, errors.Join(err, br.Close())
}

// madeFor reports whether h is the hold req, in unit, makes: the same
// subject, copies, amount, unit and attribution.
func (h hold) madeFor(req meter.ReserveRequest, unit string) bool {
	subject := req.Subject
	return h.capID == subject.CapabilityID() && h.tenantID == subject.TenantID() &&
		sameCopyIDs(copyIDs(h.copies), subject.Copies()) &&
		h.amount == req.Amount && h.unit == unit && h.op == req.Op && h.actor == req.Actor
}

// holdKeyConflict is err, the error of a hold's insert, as
// ErrReserveConflict when the hold's id is another tenant's.
func holdKeyConflict(err error, id uuid.UUID) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == constraintHoldKey {
		return fmt.Errorf("%w: %s", meter.ErrReserveConflict, id)
	}
	return err
}

// reserve is Reserve in tx, copies the encoded copies the hold is under.
func (s *UsageStore) reserve(ctx context.Context, tx pgx.Tx, req meter.ReserveRequest, copies []byte) (meter.Reservation, error) {
	subject, unit := req.Subject, req.Subject.UnitCode()
	// A replay is answered before the lineage is read: it moves nothing.
	if req.ReservationID != uuid.Nil() {
		if r, keyed, err := reserved(ctx, tx, req, unit); keyed || err != nil {
			return r, err
		}
	}
	ancestors, dbNow, err := readLineage(ctx, tx, subject.TenantID(), subject.CapabilityID())
	if err != nil {
		return meter.Reservation{}, err
	}
	c, err := s.lockCharge(ctx, tx, chargeSpec{
		capID: subject.CapabilityID(), tenantID: subject.TenantID(), unit: unit, copies: subject.Copies(), copyQuota: subject.CopyQuota(), ancestors: ancestors,
		holdID: req.ReservationID, call: meter.StoreOperationReserve,
	})
	if err != nil {
		return meter.Reservation{}, err
	}
	if c.holdTaken {
		return meter.Reservation{}, errHoldTakenAfterLock
	}
	refuse := func(err error) error { return err }
	if err := checkCeilings(c, ceilingCheck{
		amount: req.Amount, budget: subject.Budget(), unit: unit,
		capID: subject.CapabilityID(), ancestors: ancestors, copies: subject.Copies(),
	}, refuse); err != nil {
		return meter.Reservation{}, err
	}
	now := s.clock()
	expires, err := s.holdExpiry(ctx, tx, retriedReserve, subject.CapabilityID(), s.asOf(dbNow), cmp.Or(req.TTL, meter.DefaultReservationTTL))
	if err != nil {
		return meter.Reservation{}, err
	}
	h := hold{
		id: cmp.Or(req.ReservationID, uuid.New()), capID: subject.CapabilityID(), tenantID: subject.TenantID(), ancestors: ancestorIDs(ancestors),
		amount: req.Amount, unit: unit, op: req.Op, actor: req.Actor, copies: subject.Copies(),
		expires: expires.Truncate(timeResolution),
	}
	applyHold(c, h, +1)
	insert := heldStmt{sql: sqlInsertHold, args: []any{
		h.id, h.capID, h.tenantID, h.ancestors, int64(h.amount), h.unit, string(h.op), h.actor, copies, h.expires,
	}}
	if err := c.writeLast(ctx, tx, now, true, insert); err != nil {
		return meter.Reservation{}, holdKeyConflict(err, h.id)
	}
	return h.reservation(), nil
}

// capabilityExpiryGrace is how long past its record's expiry a capability
// still holds budget. The record's expiry is the issuer's clock and a hold's
// the store's — the database's, with Config.Now nil — and whether a token
// has expired is its verifier's to decide, by its own clock and leeway: a
// verifier accepts one up to limes.MaxLeeway past its expiry, by a clock up
// to limes.PurgeClockSkew behind the store's. Past both no verifier accepts
// it, and limes.MinPurgeRetention, their sum, is when its record may go.
const capabilityExpiryGrace = limes.MaxLeeway + limes.PurgeClockSkew

// holdExpiry is when a hold or a lease of capID, made at now on the store's
// clock for ttl, ends: by its capability's record's expiry and the grace at
// the latest, since past that no verifier accepts the token, no one can
// settle the hold, and it would keep the ancestors' budget held. A
// capability already past it holds nothing, refused with limes.ErrExpired;
// one past its record's expiry within the grace is the verifier's call,
// which let the call through. op names the call, for its log line.
func (s *UsageStore) holdExpiry(ctx context.Context, tx pgx.Tx, op string, capID uuid.UUID, now time.Time, ttl time.Duration) (time.Time, error) {
	expires := now.Add(ttl)
	var capExpires *time.Time
	if err := tx.QueryRow(ctx, sqlCapabilityExpiry, capID).Scan(&capExpires); err != nil {
		return time.Time{}, err
	}
	if capExpires == nil {
		return expires, nil
	}
	last := capExpires.Add(capabilityExpiryGrace)
	switch {
	case !last.After(now):
		s.log.WarnContext(ctx, "pgstore: refused a capability past its expiry and every verifier's leeway", expiredAttrs(op, capID, now.Sub(*capExpires))...)
		return time.Time{}, limes.ErrExpired
	case !capExpires.After(now):
		s.log.InfoContext(ctx, "pgstore: holding for a capability past its expiry, within a verifier's leeway", expiredAttrs(op, capID, now.Sub(*capExpires))...)
	}
	if last.Before(expires) {
		return last, nil
	}
	return expires, nil
}

// logKeyCapability is the capability a log line is about; logKeyExpiredFor
// how long past its record's expiry the store's clock stands.
const (
	logKeyCapability = "capability"
	logKeyExpiredFor = "expired_for"
)

// expiredAttrs are the attributes of holdExpiry's log lines: the call, the
// capability, and how long past its record's expiry the store's clock stands.
func expiredAttrs(op string, capID uuid.UUID, past time.Duration) []any {
	return []any{slog.String(logKeyOp, op), slog.String(logKeyCapability, capID.String()), slog.Duration(logKeyExpiredFor, past)}
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
			h, dbNow, err := lockHoldAt(ctx, tx, req.Subject.TenantID(), req.ReservationID)
			if errors.Is(err, pgx.ErrNoRows) {
				receipt, err = settled(ctx, tx, req)
				return err
			}
			if err != nil {
				return err
			}
			if !holdServes(h, req.Subject, s.asOf(dbNow)) {
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

// lockHoldAt shares tenant's erase lock, then locks and reads the hold id,
// with the database's time (sqlNow), in one batch; pgx.ErrNoRows when there
// is none. The lock is shared first, before any row is locked, as a charge
// shares it (readPrelude): a call that ends a hold writes the tenant's
// aggregate or its stripe, so SetTenantBudget and an erase, which take the
// lock alone, wait for it and it for them.
func lockHoldAt(ctx context.Context, tx pgx.Tx, tenant, id uuid.UUID) (hold, time.Time, error) {
	b := &pgx.Batch{}
	b.Queue(sqlShareTenantLock, eraseLockClass, tenant)
	b.Queue(sqlNow)
	b.Queue(sqlLockHold, id)
	br := tx.SendBatch(ctx, b)
	var dbNow time.Time
	h, err := func() (hold, error) {
		if _, err := br.Exec(); err != nil {
			return hold{}, err
		}
		if err := br.QueryRow().Scan(&dbNow); err != nil {
			return hold{}, err
		}
		return scanHold(br.QueryRow())
	}()
	return h, dbNow, errors.Join(err, br.Close())
}

// holdServes reports whether h is a hold of subject's live at asOf, under
// the copies subject presents.
func holdServes(h hold, subject limes.MeterSubject, asOf time.Time) bool {
	return h.capID == subject.CapabilityID() && h.tenantID == subject.TenantID() &&
		h.expires.After(asOf) && sameCopyIDs(copyIDs(h.copies), subject.Copies())
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
		call: meter.StoreOperationSettle,
	}
	c, err := s.lockCharge(ctx, tx, spec)
	if err != nil {
		return meter.ChargeReceipt{}, err
	}
	return s.commitCharge(ctx, tx, c, spec, onCharged)
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
			h, _, err := lockHoldAt(ctx, tx, subject.TenantID(), req.ReservationID)
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
// it, in the call's last write (writeLast); the hold's row is locked.
func (s *UsageStore) releaseHold(ctx context.Context, tx pgx.Tx, h hold) error {
	c, err := s.lock(ctx, tx, lockRequest{
		now: s.clock(), op: meter.StoreOperationRelease,
		tenantID: h.tenantID,
		existing: append([]uuid.UUID{h.capID}, h.ancestors...),
		copies:   copyIDs(h.copies), copiesCapID: h.capID,
	})
	if err != nil {
		return err
	}
	applyHold(c, h, -1)
	return c.writeLast(ctx, tx, s.clock(), c.tenant.persisted, heldStmt{sql: sqlDeleteHold, args: []any{h.id}})
}
