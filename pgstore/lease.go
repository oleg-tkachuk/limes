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

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// leaseState is a usage row's lease (migrate/ddl/migrations/032_leases.sql
// and 034_lease_requests.sql), none when its id is nil: amount held against
// the row's counters, each of ancestors and the tenant's, of which spent is
// spent, and requests held against the row's and each of ancestors, of
// which requestsSpent are made, opened in the tenant's period.
type leaseState struct {
	id                      uuid.UUID
	amount, spent           limes.Nanos
	requests, requestsSpent int64
	expires                 time.Time
	ancestors               []uuid.UUID
	period                  int64
}

// held reports whether l is a lease, open or expired.
func (l leaseState) held() bool { return l.id != uuid.Nil() }

// openAt reports whether l is a lease open at now.
func (l leaseState) openAt(now time.Time) bool { return l.held() && l.expires.After(now) }

// remaining is what l can still be charged.
func (l leaseState) remaining() limes.Nanos { return l.amount - l.spent }

// requestsRemaining is how many requests l can still be bumped.
func (l leaseState) requestsRemaining() int64 { return l.requests - l.requestsSpent }

// coversCharges and coversBumps report whether l holds budget, or
// requests, for a charge or a bump to go under.
func (l leaseState) coversCharges() bool { return l.amount > 0 }
func (l leaseState) coversBumps() bool   { return l.requests > 0 }

// leaseColumns is a lease as the usage table holds it, its columns nullable.
type leaseColumns struct {
	id            *uuid.UUID
	amount        int64
	spent         int64
	expires       *time.Time
	ancestors     []uuid.UUID
	period        int64
	requests      int64
	requestsSpent int64
}

// dest is where a row's lease columns scan, in the order the statements
// select them.
func (l *leaseColumns) dest() []any {
	return []any{&l.id, &l.amount, &l.spent, &l.expires, &l.ancestors, &l.period, &l.requests, &l.requestsSpent}
}

// state is l as a leaseState.
func (l leaseColumns) state() leaseState {
	if l.id == nil {
		return leaseState{}
	}
	s := leaseState{
		id: *l.id, amount: limes.Nanos(l.amount), spent: limes.Nanos(l.spent),
		requests: l.requests, requestsSpent: l.requestsSpent, ancestors: l.ancestors, period: l.period,
	}
	if l.expires != nil {
		s.expires = *l.expires
	}
	return s
}

// sqlLeaseColumns are the lease's columns, in leaseColumns.dest's order.
const sqlLeaseColumns = `lease_id, lease_amount, lease_spent, lease_expires, lease_ancestors, lease_period, lease_requests, lease_requests_spent`

const (
	// sqlLeasesOnChain is the lease of each capability on $1's chain that
	// holds one, read and not locked: which one a charge goes under, which
	// the lock then confirms.
	sqlLeasesOnChain = sqlChain + `
SELECT u.capability_id, u.` + sqlLeaseColumns + `
FROM limes.usage u JOIN chain ON u.capability_id = chain.id
WHERE u.lease_id IS NOT NULL`

	// sqlReadLease is $1's lease, with its tenant and unit, when it holds
	// one.
	sqlReadLease = `
SELECT tenant_id, unit_code, ` + sqlLeaseColumns + `
FROM limes.usage WHERE capability_id = $1 AND lease_id IS NOT NULL`

	// sqlSetLease writes $1's lease; its row is locked.
	sqlSetLease = `
UPDATE limes.usage
SET lease_id = $2, lease_amount = $3, lease_spent = $4, lease_expires = $5, lease_ancestors = $6, lease_period = $7,
    lease_requests = $8, lease_requests_spent = $9
WHERE capability_id = $1`

	// sqlClearLease ends $1's lease; its row is locked.
	sqlClearLease = `
UPDATE limes.usage
SET lease_id = NULL, lease_amount = 0, lease_spent = 0, lease_expires = NULL, lease_ancestors = '{}', lease_period = 0,
    lease_requests = 0, lease_requests_spent = 0
WHERE capability_id = $1`

	// sqlLeaseSpent adds $2 to what $1's lease $3 spent; its row is
	// locked.
	sqlLeaseSpent = `UPDATE limes.usage SET lease_spent = lease_spent + $2 WHERE capability_id = $1 AND lease_id = $3`

	// sqlLeaseRequestMade counts one request made under $1's lease $2; its
	// row is locked.
	sqlLeaseRequestMade = `UPDATE limes.usage SET lease_requests_spent = lease_requests_spent + 1 WHERE capability_id = $1 AND lease_id = $2`

	// sqlDueLeases are the leases past their end at $1, soonest first, at
	// most $2, with their tenants (migration 033's index).
	sqlDueLeases = `
SELECT capability_id, tenant_id FROM limes.usage
WHERE lease_id IS NOT NULL AND lease_expires <= $1
ORDER BY lease_expires, capability_id
LIMIT $2`

	// sqlTenantLeases are the leases of tenant $1.
	sqlTenantLeases = `SELECT capability_id, tenant_id FROM limes.usage WHERE tenant_id = $1 AND lease_id IS NOT NULL ORDER BY capability_id`

	// sqlLeaseOf is $1's lease and tenant, when it holds one.
	sqlLeaseOf = `SELECT capability_id, tenant_id FROM limes.usage WHERE capability_id = $1 AND lease_id IS NOT NULL`

	// sqlOrphanLeases are the leases of capabilities no longer on record, at
	// most $1.
	sqlMatchingLeasesTail = `)
ORDER BY u.capability_id
LIMIT $2`

	// sqlForgetLeaseAncestors drops $1, capabilities whose counters went,
	// from the ancestors every lease is held against, the rows locked in id
	// order: settling one later must not write a row of theirs created
	// since.
	sqlForgetLeaseAncestors = `
WITH held AS (
    SELECT capability_id FROM limes.usage WHERE lease_ancestors && $1 ORDER BY capability_id FOR UPDATE
)
UPDATE limes.usage u
SET lease_ancestors = ARRAY(SELECT a FROM unnest(u.lease_ancestors) WITH ORDINALITY AS t (a, n) WHERE a <> ALL ($1) ORDER BY n)
FROM held WHERE u.capability_id = held.capability_id`

	sqlOrphanLeases = `
SELECT u.capability_id, u.tenant_id FROM limes.usage u
WHERE u.lease_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM ` + lineageView + ` c WHERE c.id = u.capability_id)
ORDER BY u.capability_id
LIMIT $1`
)

// collectLeases reads sqlLeasesOnChain's rows.
func collectLeases(rows pgx.Rows) (map[uuid.UUID]leaseState, error) {
	out := map[uuid.UUID]leaseState{}
	for rows.Next() {
		var id uuid.UUID
		var l leaseColumns
		if err := rows.Scan(append([]any{&id}, l.dest()...)...); err != nil {
			rows.Close()
			return nil, err
		}
		out[id] = l.state()
	}
	return out, rows.Err()
}

// leasable reports whether req may be charged under a lease: one that
// refuses past a ceiling, of no Biscuit copy, whose quota of copies counts
// on every ancestor's row, which a charge under a lease does not lock. Any
// other goes through the whole chain, which the lease holds budget of too.
func leasable(req meter.ChargeRequest) bool {
	return req.Overrun != meter.OverrunPolicyRecord && len(req.Subject.Copies()) == 0
}

// leaseFor is the lease a charge or a bump of capID with ancestors goes
// under, of leases, at now: capID's own, or its nearest ancestor's, and how
// many of the ancestors are below the leaseholder; none when the nearest
// open one does not cover the call.
func leaseFor(capID uuid.UUID, ancestors []ancestor, leases map[uuid.UUID]leaseState, now time.Time, covers func(leaseState) bool) (uuid.UUID, int, bool) {
	if l := leases[capID]; l.openAt(now) {
		return capID, 0, covers(l)
	}
	for i, a := range ancestors {
		if l := leases[a.id]; l.openAt(now) {
			return a.id, i, covers(l)
		}
	}
	return uuid.Nil(), 0, false
}

// bumpLeased is bump under holder's lease, seen read before the lock: the
// capability's counter and those of the ancestors below the leaseholder
// are locked and counted as a bump counts them, the leaseholder's and its
// lease's balance with them, and nothing above. It returns the
// capability's request count after the bump, or before it when refused.
func (s *UsageStore) bumpLeased(ctx context.Context, tx pgx.Tx, subject limes.MeterSubject, ancestors []ancestor, below int, holder uuid.UUID, seen leaseState) (int64, error) {
	capID := subject.CapabilityID()
	isHolder := capID == holder
	l := lockRequest{now: s.clock(), created: append([]uuid.UUID{capID}, ancestorIDs(ancestors[:below])...), usageTenant: subject.TenantID()}
	if !isHolder {
		l.existing = []uuid.UUID{holder}
	}
	c, err := lock(ctx, tx, l)
	if err != nil {
		return 0, err
	}
	now := s.clock()
	h, ok := c.usage[holder]
	if !ok || h.lease.id != seen.id || !h.lease.openAt(now) || !h.lease.coversBumps() {
		return 0, errLeaseMoved
	}
	count := c.usageOf(capID).requests
	// The leaseholder's own ceiling admitted the lease; a capability below
	// it is held to its own, and so is every ancestor between them.
	if !isHolder {
		if err := admitRequests(c, subject, ancestors[:below], 1); err != nil {
			return count, err
		}
	}
	if h.lease.requestsRemaining() <= 0 {
		return count, fmt.Errorf("%w: %d of %d made", meter.ErrLeaseRequestsExhausted, h.lease.requestsSpent, h.lease.requests)
	}
	countRequest(c, subject.UnitCode())
	h.reservedRequests = max(h.reservedRequests-1, 0)
	made := heldStmt{sql: sqlLeaseRequestMade, args: []any{holder, h.lease.id}}
	return count + 1, c.write(ctx, tx, now, false, made)
}

// errLeaseMoved is a lease read before its row was locked that was settled,
// expired or opened anew meanwhile: the call runs again, reading it afresh.
var errLeaseMoved = fmt.Errorf("%w: pgstore: a lease moved while the call locked it", limes.ErrInternal)

// errLeaseStale is a Lease of a capability whose own lease expired, or
// whose chain changed, since it opened: it is settled first, and a new one
// opened.
var errLeaseStale = fmt.Errorf("%w: pgstore: a lease to settle before another opens", limes.ErrInternal)

// chargeLeased is charge under holder's lease, seen read before the lock:
// the capability's counters and those of the ancestors below the
// leaseholder are locked and spent as a charge spends them, the
// leaseholder's and its lease's balance with them, and nothing above — not
// the ancestors the lease is held against, not the tenant's aggregate.
func (s *UsageStore) chargeLeased(ctx context.Context, tx pgx.Tx, req meter.ChargeRequest, unit string, ancestors []ancestor, below int, holder uuid.UUID, seen leaseState, onCharged func(context.Context, meter.Charged) error) (meter.ChargeReceipt, error) {
	capID, tenantID := req.Subject.CapabilityID(), req.Subject.TenantID()
	isHolder := capID == holder
	l := lockRequest{now: s.clock(), created: append([]uuid.UUID{capID}, ancestorIDs(ancestors[:below])...), usageTenant: tenantID}
	if !isHolder {
		l.existing = []uuid.UUID{holder}
	}
	c, err := lock(ctx, tx, l)
	if err != nil {
		return meter.ChargeReceipt{}, err
	}
	now := s.clock()
	h, ok := c.usage[holder]
	if !ok || h.lease.id != seen.id || !h.lease.openAt(now) {
		return meter.ChargeReceipt{}, errLeaseMoved
	}
	// Another delivery of the same reference may have committed while this
	// one waited for the locks: its receipt is the answer.
	if req.ExternalRef != "" {
		e, found, err := readEntry(ctx, tx, sqlChargeByRef, capID, req.ExternalRef)
		if err != nil {
			return meter.ChargeReceipt{}, err
		}
		if found {
			r, _, err := receiptOfRef(ctx, tx, req, unit, e)
			return r, cmp.Or(err, errReplayedAfterLock)
		}
	}
	refused := meter.ChargeReceipt{SpentAmount: c.usageOf(capID).spent, TenantID: tenantID, UnitCode: unit}
	if err := admitLeased(c, req, unit, ancestors[:below], h, isHolder); err != nil {
		return refused, err
	}
	receipt := meter.ChargeReceipt{ChargeID: uuid.New(), SpentAmount: c.usageOf(capID).spent + req.Amount, TenantID: tenantID, UnitCode: unit}
	if err := runOnCharged(ctx, tx, receipt, onCharged); err != nil {
		return refused, err
	}
	for _, id := range l.created {
		u := c.usage[id]
		u.spent += req.Amount
		u.unit = cmp.Or(u.unit, unit)
	}
	if !isHolder {
		h.spent += req.Amount
	}
	h.reserved = max0(h.reserved - req.Amount)
	insert := heldStmt{sql: sqlInsertCharge, args: []any{
		receipt.ChargeID, capID, tenantID, int64(req.Amount), unit, string(req.Op), req.Actor, now,
		ancestorIDs(ancestors), [][]byte{}, req.ExternalRef, nil, false, h.lease.period, h.lease.id, holder,
	}}
	spentUnder := heldStmt{sql: sqlLeaseSpent, args: []any{holder, int64(req.Amount), h.lease.id}}
	if err := c.write(ctx, tx, now, false, insert, spentUnder); err != nil {
		return refused, err
	}
	return receipt, nil
}

// admitLeased refuses a charge under the lease of h: past the capability's
// own ceiling or an ancestor's below the leaseholder — the leaseholder's own
// admitted the lease — or past what the lease holds.
func admitLeased(c *counters, req meter.ChargeRequest, unit string, below []ancestor, h *usageRow, isHolder bool) error {
	if h.unit != "" && h.unit != unit {
		return fmt.Errorf("%w: a charge in %q under a lease in %q", limes.ErrUnitCodeMismatch, unit, h.unit)
	}
	k := ceilingCheck{amount: req.Amount, budget: req.Subject.Budget(), unit: unit, capID: req.Subject.CapabilityID(), ancestors: below}
	if err := checkUnits(c, k); err != nil {
		return err
	}
	if err := checkRange(c, k); err != nil {
		return err
	}
	refuse := func(err error) error { return err }
	if !isHolder {
		if err := checkCapabilityCeiling(c, k, refuse); err != nil {
			return err
		}
		if err := checkAncestorCeilings(c, k, refuse); err != nil {
			return err
		}
	}
	if h.lease.spent+req.Amount > h.lease.amount {
		return fmt.Errorf("%w: %s of %s left, %s asked", meter.ErrLeaseExhausted, h.lease.remaining(), h.lease.amount, req.Amount)
	}
	return nil
}

// Lease implements meter.Leaser in one transaction scoped to the subject's
// tenant: the chain's counters locked and the tenant's aggregate last, as a
// Reserve locks them. A lease to settle first is settled in a transaction of
// its own, and the lease opened in another.
func (s *UsageStore) Lease(ctx context.Context, req meter.LeaseRequest) (meter.Lease, error) {
	if err := req.Validate(); err != nil {
		return meter.Lease{}, err
	}
	if !s.leases {
		s.log.WarnContext(ctx, "pgstore: lease refused: Config.Leases is not set", slog.String(logKeyOp, retriedLease))
		return meter.Lease{}, meter.ErrLeasingDisabled
	}
	var out meter.Lease
	open := func() error {
		return s.retryDeadlock(ctx, retriedLease, func() error {
			return s.inTenantTx(ctx, req.Subject.TenantID(), func(tx pgx.Tx) error {
				var err error
				out, err = s.lease(ctx, tx, req)
				return err
			})
		})
	}
	err := open()
	if errors.Is(err, errLeaseStale) {
		if _, err := s.settle(ctx, req.Subject.CapabilityID(), req.Subject.TenantID()); err != nil && !errors.Is(err, meter.ErrLeaseNotFound) {
			return meter.Lease{}, err
		}
		err = open()
	}
	return out, err
}

// lease is Lease in tx.
func (s *UsageStore) lease(ctx context.Context, tx pgx.Tx, req meter.LeaseRequest) (meter.Lease, error) {
	subject, unit := req.Subject, req.Subject.UnitCode()
	capID, tenantID := subject.CapabilityID(), subject.TenantID()
	ancestors, err := readLineage(ctx, tx, tenantID, capID)
	if err != nil {
		return meter.Lease{}, err
	}
	now := s.clock()
	held := append([]uuid.UUID{capID}, ancestorIDs(ancestors)...)
	// The tenant's aggregate is locked for its period; a lease of requests
	// alone, which holds none of its budget, does not create it.
	locks := lockRequest{now: now, tenantID: tenantID, created: held, usageTenant: tenantID}
	if req.Amount > 0 {
		locks.tenantUnit = unit
	}
	c, err := lock(ctx, tx, locks)
	if err != nil {
		return meter.Lease{}, err
	}
	for _, a := range ancestors {
		if c.usageOf(a.id).lease.openAt(now) {
			return meter.Lease{}, fmt.Errorf("%w: %s holds one above %s", meter.ErrLeaseNested, a.id, capID)
		}
	}
	self := c.usage[capID]
	// A lease opened in a period the tenant has since closed is settled
	// before an amount is added: what it spends from now on is this
	// period's, and settlement counts a lease's spend in its own period.
	closed := req.Amount > 0 && self.lease.period != c.tenant.period
	if self.lease.held() && (!self.lease.openAt(now) || !slices.Equal(self.lease.ancestors, ancestorIDs(ancestors)) || closed) {
		return meter.Lease{}, errLeaseStale
	}
	refuse := func(err error) error { return err }
	if err := checkCeilings(c, ceilingCheck{amount: req.Amount, budget: subject.Budget(), unit: unit, capID: capID, ancestors: ancestors}, refuse); err != nil {
		return meter.Lease{}, err
	}
	if err := admitRequests(c, subject, ancestors, req.Requests); err != nil {
		return meter.Lease{}, err
	}
	expires, err := holdExpiry(ctx, tx, capID, now, cmp.Or(req.TTL, meter.DefaultLeaseTTL))
	if err != nil {
		return meter.Lease{}, err
	}
	l := self.lease
	if !l.held() {
		l = leaseState{id: uuid.New(), ancestors: ancestorIDs(ancestors), period: c.tenant.period}
	}
	l.amount += req.Amount
	l.requests += req.Requests
	if expires = expires.Truncate(timeResolution); expires.After(l.expires) {
		l.expires = expires
	}
	for _, id := range held {
		u := c.usage[id]
		u.reserved += req.Amount
		u.reservedRequests += req.Requests
		u.unit = cmp.Or(u.unit, unit)
	}
	// A lease of requests alone holds nothing of the tenant's, whose
	// aggregate it leaves as it is.
	c.tenant.reserved += req.Amount
	set := heldStmt{sql: sqlSetLease, args: []any{capID, l.id, int64(l.amount), int64(l.spent), l.expires, l.ancestors, l.period, l.requests, l.requestsSpent}}
	if err := c.write(ctx, tx, now, req.Amount > 0, set); err != nil {
		return meter.Lease{}, err
	}
	return leaseView(capID, tenantID, unit, l), nil
}

// leaseView is l as meter.Lease gives it.
func leaseView(capID, tenantID uuid.UUID, unit string, l leaseState) meter.Lease {
	return meter.Lease{
		ID: l.id, CapabilityID: capID, TenantID: tenantID, UnitCode: unit, Amount: l.amount, Spent: l.spent,
		Requests: l.requests, RequestsSpent: l.requestsSpent, ExpiresAt: l.expires,
	}
}

// GetLease implements meter.Leaser, read under the call's scope.
func (s *UsageStore) GetLease(ctx context.Context, capID uuid.UUID) (meter.Lease, error) {
	var out meter.Lease
	err := s.inReadTx(ctx, func(tx pgx.Tx) error {
		var tenantID uuid.UUID
		var unit string
		var l leaseColumns
		if err := tx.QueryRow(ctx, sqlReadLease, capID).Scan(append([]any{&tenantID, &unit}, l.dest()...)...); err != nil {
			return err
		}
		out = leaseView(capID, tenantID, unit, l.state())
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return meter.Lease{}, meter.ErrLeaseNotFound
	}
	return out, err
}

// SettleLease implements meter.Leaser, in one transaction scoped to the
// subject's tenant.
func (s *UsageStore) SettleLease(ctx context.Context, req meter.SettleLeaseRequest) (meter.Lease, error) {
	if err := req.Validate(); err != nil {
		return meter.Lease{}, err
	}
	return s.settle(ctx, req.Subject.CapabilityID(), req.Subject.TenantID())
}

// settle settles capID's lease in a transaction of its own, scoped to its
// tenant, run again once should the lease move while it was locked.
func (s *UsageStore) settle(ctx context.Context, capID, tenantID uuid.UUID) (meter.Lease, error) {
	var out meter.Lease
	attempt := func() error {
		return s.retryDeadlock(ctx, retriedSettleLease, func() error {
			return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
				var err error
				out, err = s.settleLease(ctx, tx, capID)
				return err
			})
		})
	}
	err := attempt()
	if errors.Is(err, errLeaseMoved) {
		err = attempt()
	}
	return out, err
}

// settleLease ends capID's lease in tx: what it spent is charged to its
// ancestors and its tenant — the tenant's while the lease's period is still
// the current one — and the requests it made counted on its ancestors; what
// it holds is released from them and from its capability, which spent its
// share, and counted its requests, as they were made. The lease is read,
// then its counters locked in the one order every call takes and the
// tenant's aggregate last, and errLeaseMoved returned should it have moved
// between.
func (s *UsageStore) settleLease(ctx context.Context, tx pgx.Tx, capID uuid.UUID) (meter.Lease, error) {
	var tenantID uuid.UUID
	var unit string
	var read leaseColumns
	err := tx.QueryRow(ctx, sqlReadLease, capID).Scan(append([]any{&tenantID, &unit}, read.dest()...)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return meter.Lease{}, meter.ErrLeaseNotFound
	}
	if err != nil {
		return meter.Lease{}, err
	}
	seen := read.state()
	now := s.clock()
	c, err := lock(ctx, tx, lockRequest{now: now, tenantID: tenantID, existing: append([]uuid.UUID{capID}, seen.ancestors...)})
	if err != nil {
		return meter.Lease{}, err
	}
	self, ok := c.usage[capID]
	if !ok || self.lease.id != seen.id {
		return meter.Lease{}, errLeaseMoved
	}
	l := self.lease
	self.reserved = max0(self.reserved - l.remaining())
	self.reservedRequests = max(self.reservedRequests-l.requestsRemaining(), 0)
	for _, id := range l.ancestors {
		if u, ok := c.usage[id]; ok {
			u.reserved = max0(u.reserved - l.amount)
			u.spent += l.spent
			u.reservedRequests = max(u.reservedRequests-l.requests, 0)
			u.requests += l.requestsSpent
		}
	}
	if c.tenant.persisted {
		c.tenant.reserved = max0(c.tenant.reserved - l.amount)
		if l.period == c.tenant.period {
			c.tenant.spent += l.spent
		}
	}
	end := heldStmt{sql: sqlClearLease, args: []any{capID}}
	if err := c.write(ctx, tx, now, c.tenant.persisted, end); err != nil {
		return meter.Lease{}, err
	}
	return leaseView(capID, tenantID, unit, l), nil
}

// sqlMatchingLeasesHead and sqlMatchingLeasesTail surround the selectors of
// ReleaseMatching, on the capability c: the leases of tenant $1 — every
// tenant's, $1 being NULL — at most $2, each of a revoked capability that
// is c or was delegated from it, over the delegation tree on record rather
// than lease_ancestors, which drops the capabilities whose counters went.
var sqlMatchingLeasesHead = `
SELECT u.capability_id, u.tenant_id FROM limes.usage u
JOIN limes.capabilities h ON h.id = u.capability_id
WHERE u.lease_id IS NOT NULL AND ($1::uuid IS NULL OR u.tenant_id = $1) AND h.revoked
  AND EXISTS (
    SELECT 1 FROM limes.capabilities c
    WHERE c.id IN (` + sqlChainUp("u.capability_id") + `) AND c.tenant_id = u.tenant_id`

// leaseholder is a capability holding a lease, and its tenant.
type leaseholder struct {
	capID, tenantID uuid.UUID
}

// settleEach settles the lease of each holder query names, each in a
// transaction of its own, and returns those it settled, and how many query
// named: one settled meanwhile is no error.
func (s *UsageStore) settleEach(ctx context.Context, inTx func(func(pgx.Tx) error) error, query string, args ...any) ([]leaseholder, int, error) {
	var holders []leaseholder
	err := inTx(func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		holders, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (leaseholder, error) {
			var h leaseholder
			return h, row.Scan(&h.capID, &h.tenantID)
		})
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	var settled []leaseholder
	for _, h := range holders {
		if _, err := s.settle(ctx, h.capID, h.tenantID); err != nil {
			if errors.Is(err, meter.ErrLeaseNotFound) {
				continue
			}
			return settled, len(holders), err
		}
		settled = append(settled, h)
	}
	return settled, len(holders), nil
}

// refundLeased gives amount of e, a charge taken under a lease still open,
// back to what it debited: the capability, the ancestors up to the
// leaseholder, the leaseholder's hold and the lease's balance. It reports
// false when e was taken under no lease or one since settled, whose refund
// goes to the chain the settlement charged; c holds the chain's counters.
func (s *UsageStore) refundLeased(ctx context.Context, tx pgx.Tx, c *counters, e ledgerEntry, amount limes.Nanos) (bool, error) {
	if e.leaseID == nil || e.leaseHolder == nil {
		return false, nil
	}
	h, ok := c.usage[*e.leaseHolder]
	if !ok || h.lease.id != *e.leaseID {
		return false, nil
	}
	ids := []uuid.UUID{e.CapabilityID}
	if e.CapabilityID != *e.leaseHolder {
		ids = append(ids, e.ancestors[:slices.Index(e.ancestors, *e.leaseHolder)+1]...)
	}
	only := &counters{usage: map[uuid.UUID]*usageRow{}, copies: map[string]*copyRow{}}
	for _, id := range ids {
		if u, ok := c.usage[id]; ok && e.counted(u.born) {
			u.spent = max0(u.spent - amount)
			only.usage[id] = u
		}
	}
	h.reserved += amount
	only.usage[h.id] = h
	refunded := heldStmt{sql: sqlRefunded, args: []any{e.ID, int64(amount)}}
	back := heldStmt{sql: sqlLeaseSpent, args: []any{h.id, -int64(amount), h.lease.id}}
	return true, only.write(ctx, tx, s.clock(), false, refunded, back)
}

// forgetLeaseAncestors drops ids, whose counters went, from every lease's
// ancestors, in a transaction of its own.
func (s *UsageStore) forgetLeaseAncestors(ctx context.Context, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, sqlForgetLeaseAncestors, ids)
		return err
	})
}

var _ meter.Leaser = (*UsageStore)(nil)
