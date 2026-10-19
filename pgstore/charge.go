package pgstore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
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
	unit := req.Subject.UnitCode()
	subject := req.Subject
	var receipt meter.BumpReceipt
	err := s.inTenantTx(ctx, subject.TenantID(), func(tx pgx.Tx) error {
		if err := notErased(ctx, tx, subject.TenantID()); err != nil {
			return err
		}
		ancestors, err := lineage(ctx, tx, subject.CapabilityID())
		if err != nil {
			return err
		}
		c, err := lock(ctx, tx, lockRequest{
			now:     s.clock(),
			created: append([]uuid.UUID{subject.CapabilityID()}, ancestorIDs(ancestors)...), usageTenant: subject.TenantID(),
			copies: copyIDs(subject.Copies()), createCopies: true, copiesCapID: subject.CapabilityID(), maxCopies: subject.CopyQuota().Bounded(s.maxCopies),
		})
		if err != nil {
			return err
		}
		u := c.usage[subject.CapabilityID()]
		receipt.RequestCount = u.requests
		next := u.requests + 1
		for _, cp := range subject.Copies() {
			if !cp.AdmitsRequests(c.copyOf(cp.RevocationID).requests + 1) {
				return fmt.Errorf("%w: copy %x", limes.ErrRequestLimitExceeded, cp.RevocationID)
			}
		}
		if !subject.Requests().Admits(next) {
			return limes.ErrRequestLimitExceeded
		}
		for _, a := range ancestors {
			if !a.requests.Admits(c.usageOf(a.id).requests + 1) {
				return fmt.Errorf("%w: ancestor %s", limes.ErrRequestLimitExceeded, a.id)
			}
		}
		for _, row := range c.usage {
			row.requests++
			if row.unit == "" {
				row.unit = unit
			}
		}
		for _, row := range c.copies {
			row.requests++
		}
		receipt.RequestCount = next
		return c.write(ctx, tx, s.clock(), false)
	})
	if err != nil {
		return meter.BumpReceipt{RequestCount: receipt.RequestCount}, err
	}
	return receipt, nil
}

const (
	sqlChargeByRef = `
SELECT id, capability_id, tenant_id, amount, refunded, unit_code, external_ref, overrun, op, actor, at,
       ancestors, copies, reservation_id, tenant_period
FROM limes.charges WHERE capability_id = $1 AND external_ref = $2 AND external_ref <> ''`

	sqlChargeByID = `
SELECT id, capability_id, tenant_id, amount, refunded, unit_code, external_ref, overrun, op, actor, at,
       ancestors, copies, reservation_id, tenant_period
FROM limes.charges WHERE id = $1`

	sqlChargeByReservation = `
SELECT id, capability_id, tenant_id, amount, refunded, unit_code, external_ref, overrun, op, actor, at,
       ancestors, copies, reservation_id, tenant_period
FROM limes.charges WHERE reservation_id = $1`

	sqlInsertCharge = `
INSERT INTO limes.charges
    (id, capability_id, tenant_id, amount, unit_code, op, actor, at, ancestors, copies,
     external_ref, reservation_id, overrun, tenant_period)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`

	sqlSpent = `SELECT spent FROM limes.usage WHERE capability_id = $1`
)

// ledgerEntry is one row of the charges ledger.
type ledgerEntry struct {
	meter.ChargeRecord
	ancestors     []uuid.UUID
	copies        [][]byte
	reservationID *uuid.UUID
	tenantPeriod  int64
}

// readEntry reads the one ledger row query names, if there is one.
func readEntry(ctx context.Context, tx pgx.Tx, query string, args ...any) (ledgerEntry, bool, error) {
	var e ledgerEntry
	var amount, refunded int64
	var op string
	err := tx.QueryRow(ctx, query, args...).Scan(
		&e.ID, &e.CapabilityID, &e.TenantID, &amount, &refunded, &e.UnitCode, &e.ExternalRef, &e.Overrun,
		&op, &e.Actor, &e.At, &e.ancestors, &e.copies, &e.reservationID, &e.tenantPeriod)
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

// replayByRef answers a charge whose (capability, ExternalRef) is already in
// the ledger: its receipt again, or ErrExternalRefConflict when it names
// another cost.
func replayByRef(ctx context.Context, tx pgx.Tx, req meter.ChargeRequest, unit string) (meter.ChargeReceipt, bool, error) {
	if req.ExternalRef == "" {
		return meter.ChargeReceipt{}, false, nil
	}
	e, ok, err := readEntry(ctx, tx, sqlChargeByRef, req.Subject.CapabilityID(), req.ExternalRef)
	if err != nil || !ok {
		return meter.ChargeReceipt{}, false, err
	}
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
	unit := req.Subject.UnitCode()
	var receipt meter.ChargeReceipt
	err := s.inTenantTx(ctx, req.Subject.TenantID(), func(tx pgx.Tx) error {
		// A replay is answered before the lineage is read: it moves nothing,
		// so it needs no record, and a capability purged since still gets its
		// receipt, as a settled hold does.
		if err := notErased(ctx, tx, req.Subject.TenantID()); err != nil {
			return err
		}
		r, replayed, err := replayByRef(ctx, tx, req, unit)
		if replayed || err != nil {
			receipt = r
			return err
		}
		ancestors, err := lineage(ctx, tx, req.Subject.CapabilityID())
		if err != nil {
			return err
		}
		spec := chargeSpec{
			capID: req.Subject.CapabilityID(), tenantID: req.Subject.TenantID(), budget: req.Subject.Budget(),
			unit: unit, copies: req.Subject.Copies(), copyQuota: req.Subject.CopyQuota(), amount: req.Amount, op: req.Op, actor: req.Actor,
			externalRef: req.ExternalRef, overrun: req.Overrun, ancestors: ancestors,
		}
		c, err := s.lockCharge(ctx, tx, spec)
		if err != nil {
			return err
		}
		// Another delivery of the same reference may have committed while
		// this one waited for the locks.
		if r, replayed, err := replayByRef(ctx, tx, req, unit); replayed || err != nil {
			receipt = r
			return err
		}
		receipt, err = s.commitCharge(ctx, tx, c, spec, onCharged)
		return err
	})
	return receipt, err
}

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
	}
	if spec.settling != nil {
		l.existing = spec.settling.ancestors
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
	if onCharged != nil {
		callbackCtx, end := withTx(ctx, tx)
		err := func() error {
			defer end()
			return onCharged(callbackCtx, meter.Charged{Receipt: receipt})
		}()
		if err != nil {
			return refused, err
		}
	}

	u := c.usage[spec.capID]
	u.spent += spec.amount
	u.reserved = max0(u.reserved - released)
	u.unit = spec.unit
	for _, anc := range spec.ancestors {
		au := c.usage[anc.id]
		au.spent += spec.amount
		if au.unit == "" {
			au.unit = spec.unit
		}
	}
	for _, id := range holders {
		if au, ok := c.usage[id]; ok {
			au.reserved = max0(au.reserved - released)
		}
	}
	c.tenant.spent += spec.amount
	c.tenant.reserved = max0(c.tenant.reserved - released)
	if c.tenant.unit == "" {
		c.tenant.unit = spec.unit
	}
	for _, cp := range spec.copies {
		r := c.copies[string(cp.RevocationID)]
		r.spent += spec.amount
		r.reserved = max0(r.reserved - released)
	}
	now := s.clock()
	if err := c.write(ctx, tx, now, true); err != nil {
		return refused, err
	}
	if _, err := tx.Exec(ctx, sqlInsertCharge,
		receipt.ChargeID, spec.capID, spec.tenantID, int64(spec.amount), spec.unit, string(spec.op), spec.actor, now,
		ancestorIDs(spec.ancestors), copyIDs(spec.copies), spec.externalRef, reservationID, overrun, c.tenant.period); err != nil {
		return refused, err
	}
	return receipt, nil
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
	// A settle at or below its hold adds nothing to a counter that holds it,
	// so no ceiling there can refuse it — not even one crossed since the
	// hold was made. Only the excess must fit. An ancestor that does not
	// hold it takes the whole amount.
	withinHold := k.amount <= k.released
	if !withinHold {
		for _, cp := range k.copies {
			r := c.copyOf(cp.RevocationID)
			if !cp.AdmitsBudget(r.spent + r.reserved - k.released + k.amount) {
				if err := crossed(fmt.Errorf("%w: copy %x", limes.ErrBudgetExceeded, cp.RevocationID)); err != nil {
					return err
				}
			}
		}
		u := c.usageOf(k.capID)
		if !k.budget.Admits(u.spent + u.reserved - k.released + k.amount) {
			if err := crossed(limes.ErrBudgetExceeded); err != nil {
				return err
			}
		}
	}
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
	b := c.tenant
	if !withinHold && !b.ceiling.Admits(b.spent+k.amount+max0(b.reserved-k.released)) {
		if err := crossed(limes.ErrTenantBudgetExceeded); err != nil {
			return err
		}
	}
	return nil
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
	unit := req.Subject.UnitCode()
	ttl := req.TTL
	if ttl == 0 {
		ttl = meter.DefaultReservationTTL
	}
	subject := req.Subject
	copies, err := encodeHeldCopies(subject.Copies())
	if err != nil {
		return meter.Reservation{}, err
	}
	var out meter.Reservation
	err = s.inTenantTx(ctx, subject.TenantID(), func(tx pgx.Tx) error {
		if err := notErased(ctx, tx, subject.TenantID()); err != nil {
			return err
		}
		ancestors, err := lineage(ctx, tx, subject.CapabilityID())
		if err != nil {
			return err
		}
		c, err := s.lockCharge(ctx, tx, chargeSpec{
			capID: subject.CapabilityID(), tenantID: subject.TenantID(), unit: unit, copies: subject.Copies(), copyQuota: subject.CopyQuota(), ancestors: ancestors,
		})
		if err != nil {
			return err
		}
		refuse := func(err error) error { return err }
		if err := checkCeilings(c, ceilingCheck{
			amount: req.Amount, budget: subject.Budget(), unit: unit,
			capID: subject.CapabilityID(), ancestors: ancestors, copies: subject.Copies(),
		}, refuse); err != nil {
			return err
		}
		now := s.clock()
		// A hold ends with its capability at the latest: past it no one can
		// settle the hold, and it would keep the ancestors' budget held.
		expires := now.Add(ttl)
		var capExpires *time.Time
		if err := tx.QueryRow(ctx, sqlCapabilityExpiry, subject.CapabilityID()).Scan(&capExpires); err != nil {
			return err
		}
		// A capability already expired could hold nothing anyone may settle.
		if capExpires != nil && !capExpires.After(now) {
			return limes.ErrExpired
		}
		if capExpires != nil && capExpires.Before(expires) {
			expires = *capExpires
		}
		h := hold{
			id: uuid.New(), capID: subject.CapabilityID(), tenantID: subject.TenantID(), ancestors: ancestorIDs(ancestors),
			amount: req.Amount, unit: unit, op: req.Op, actor: req.Actor, copies: subject.Copies(),
			expires: expires.Truncate(timeResolution),
		}
		applyHold(c, h, +1)
		if err := c.write(ctx, tx, now, true); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, sqlInsertHold,
			h.id, h.capID, h.tenantID, h.ancestors, int64(h.amount), h.unit, string(h.op), h.actor, copies, h.expires); err != nil {
			return err
		}
		out = h.reservation()
		return nil
	})
	return out, err
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
	if req.ReservationID == uuid.Nil {
		return meter.ChargeReceipt{}, meter.ErrReservationNotFound
	}
	subject := req.Subject
	var receipt meter.ChargeReceipt
	err := s.inTenantTx(ctx, subject.TenantID(), func(tx pgx.Tx) error {
		h, err := scanHold(tx.QueryRow(ctx, sqlLockHold, req.ReservationID))
		if errors.Is(err, pgx.ErrNoRows) {
			// A reservation of another capability or tenant, or settled
			// under other copies, is none of this one's: its receipt is not
			// this subject's to read back either.
			e, settled, err := readEntry(ctx, tx, sqlChargeByReservation, req.ReservationID)
			if err != nil {
				return err
			}
			if settled && e.CapabilityID == subject.CapabilityID() && e.TenantID == subject.TenantID() && sameCopyIDs(e.copies, subject.Copies()) {
				if e.Amount != req.Amount {
					return fmt.Errorf("%w: %s", meter.ErrSettleConflict, req.ReservationID)
				}
				receipt, err = replay(ctx, tx, e)
				return err
			}
			return meter.ErrReservationNotFound
		}
		if err != nil {
			return err
		}
		if h.capID != subject.CapabilityID() || h.tenantID != subject.TenantID() || !h.expires.After(s.clock()) || !sameCopyIDs(copyIDs(h.copies), subject.Copies()) {
			return meter.ErrReservationNotFound
		}
		ancestors, err := lineage(ctx, tx, h.capID)
		if err != nil {
			return err
		}
		// The charge is the reservation's: its tenant, unit and the copy
		// limits it was made under, against the subject's own ceiling.
		spec := chargeSpec{
			capID: h.capID, tenantID: h.tenantID, budget: subject.Budget(), unit: h.unit, copies: h.copies, copyQuota: subject.CopyQuota(),
			amount: req.Amount, op: h.op, actor: h.actor, overrun: req.Overrun, ancestors: ancestors, settling: &h,
		}
		c, err := s.lockCharge(ctx, tx, spec)
		if err != nil {
			return err
		}
		receipt, err = s.commitCharge(ctx, tx, c, spec, onCharged)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, sqlDeleteHold, h.id)
		return err
	})
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
	if err := c.write(ctx, tx, s.clock(), c.tenant.persisted); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, sqlDeleteHold, h.id)
	return err
}
