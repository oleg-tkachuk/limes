package memstore

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// lease is budget and requests a capability holds of its chain
// (meter.Leaser): amount held against its own counters, each of ancestors
// and its tenant's, of which spent is spent, and requests held against its
// own and each of ancestors, of which requestsSpent are made.
type lease struct {
	id              uuid.UUID
	capID, tenantID uuid.UUID
	// ancestors are those the lease is held against, nearest first, as the
	// lineage stood when it opened; one dropped since is forgotten
	// (forgetAncestorLocked).
	ancestors               []uuid.UUID
	unit                    string
	amount, spent           limes.Nanos
	requests, requestsSpent int64
	expires                 time.Time
	// period is the tenant's accounting period the lease opened in: its
	// spend is the period's, and a reset settles it first.
	period uint64
}

// view is l as meter.Lease gives it.
func (l lease) view() meter.Lease {
	return meter.Lease{
		ID: l.id, CapabilityID: l.capID, TenantID: l.tenantID, UnitCode: l.unit,
		Amount: l.amount, Spent: l.spent, Requests: l.requests, RequestsSpent: l.requestsSpent,
		ExpiresAt: l.expires,
	}
}

// openAt reports whether l is open at now: not expired.
func (l lease) openAt(now time.Time) bool {
	return l.expires.After(now)
}

// Lease implements meter.Leaser.
func (s *UsageStore) Lease(_ context.Context, req meter.LeaseRequest) (meter.Lease, error) {
	if err := req.Validate(); err != nil {
		return meter.Lease{}, err
	}
	subject, unit := req.Subject, req.Subject.UnitCode()
	capID, tenantID := subject.CapabilityID(), subject.TenantID()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.notErasedLocked(tenantID); err != nil {
		return meter.Lease{}, err
	}
	ancestors, err := s.lineage(capID)
	if err != nil {
		return meter.Lease{}, err
	}
	now := s.nowFn()
	for _, a := range ancestors {
		if l, ok := s.leases[a.ID]; ok && l.openAt(now) {
			return meter.Lease{}, fmt.Errorf("%w: %s holds one above %s", meter.ErrLeaseNested, a.ID, capID)
		}
	}
	// A lease that expired, or whose chain changed, is settled and a new one
	// opened: a top-up holds against the chain the lease holds against.
	if l, ok := s.leases[capID]; ok && (!l.openAt(now) || !slices.Equal(l.ancestors, ancestorIDs(ancestors))) {
		s.settleLeaseLocked(l)
	}
	refuse := func(err error) error { return err }
	if err := s.checkCeilingsLocked(ceilingCheck{
		amount: req.Amount, maxBudget: subject.Budget(), unit: unit,
		capID: capID, tenantID: tenantID, ancestors: ancestors,
	}, refuse); err != nil {
		return meter.Lease{}, err
	}
	if err := s.admitRequestsLocked(subject, ancestors, req.Requests); err != nil {
		return meter.Lease{}, err
	}
	expires, err := s.holdExpiry(capID, cmp.Or(req.TTL, meter.DefaultLeaseTTL))
	if err != nil {
		return meter.Lease{}, err
	}
	l, ok := s.leases[capID]
	if !ok {
		l = lease{
			id: uuid.New(), capID: capID, tenantID: tenantID, ancestors: ancestorIDs(ancestors),
			unit: unit, period: s.periods[tenantID],
		}
	}
	l.amount += req.Amount
	l.requests += req.Requests
	if expires.After(l.expires) {
		l.expires = expires
	}
	if req.Amount > 0 {
		s.reserveLeaseLocked(l, req.Amount, +1)
	}
	s.holdLeaseRequestsLocked(l, req.Requests)
	s.leases[capID] = l
	return l.view(), nil
}

// reserveLeaseLocked adds (sign +1) or removes (sign -1) amount held by l
// on its capability, its ancestors and its tenant. s.mu must be held.
func (s *UsageStore) reserveLeaseLocked(l lease, amount, sign limes.Nanos) {
	for _, id := range append([]uuid.UUID{l.capID}, l.ancestors...) {
		u := s.usage[id]
		u.CapabilityID = id
		u.ReservedAmount = max0(u.ReservedAmount + sign*amount)
		u.UnitCode = cmp.Or(u.UnitCode, l.unit)
		s.putUsageLocked(id, l.tenantID, u)
	}
	b := s.tenantLocked(l.tenantID, l.unit)
	b.ReservedAmount = max0(b.ReservedAmount + sign*amount)
	b.UpdatedAt = s.nowFn()
	s.budgets[l.tenantID] = b
}

// holdLeaseRequestsLocked holds n more requests of l on its capability and
// its ancestors. s.mu must be held.
func (s *UsageStore) holdLeaseRequestsLocked(l lease, n int64) {
	for _, id := range append([]uuid.UUID{l.capID}, l.ancestors...) {
		u := s.usage[id]
		u.CapabilityID = id
		u.ReservedRequests += n
		u.UnitCode = cmp.Or(u.UnitCode, l.unit)
		s.putUsageLocked(id, l.tenantID, u)
	}
}

// GetLease implements meter.Leaser.
func (s *UsageStore) GetLease(_ context.Context, capID uuid.UUID) (meter.Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.leases[capID]
	if !ok {
		return meter.Lease{}, meter.ErrLeaseNotFound
	}
	return l.view(), nil
}

// SettleLease implements meter.Leaser.
func (s *UsageStore) SettleLease(_ context.Context, req meter.SettleLeaseRequest) (meter.Lease, error) {
	if err := req.Validate(); err != nil {
		return meter.Lease{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.leases[req.Subject.CapabilityID()]
	if !ok || l.tenantID != req.Subject.TenantID() {
		return meter.Lease{}, meter.ErrLeaseNotFound
	}
	s.settleLeaseLocked(l)
	return l.view(), nil
}

// settleLeaseLocked ends l: what it spent is charged to its ancestors and
// its tenant — the tenant's while the lease's period is still the current
// one — and the requests it made counted on its ancestors; what it holds
// is released from them and from its capability, which spent its share,
// and counted its requests, as they were made. s.mu must be held.
func (s *UsageStore) settleLeaseLocked(l lease) {
	if u, ok := s.usage[l.capID]; ok {
		u.ReservedAmount = max0(u.ReservedAmount - l.Remaining())
		u.ReservedRequests = max(u.ReservedRequests-l.requestsRemaining(), 0)
		s.putUsageLocked(l.capID, l.tenantID, u)
	}
	for _, id := range l.ancestors {
		if u, ok := s.usage[id]; ok {
			u.ReservedAmount = max0(u.ReservedAmount - l.amount)
			u.SpentAmount += l.spent
			u.ReservedRequests = max(u.ReservedRequests-l.requests, 0)
			u.RequestCount += l.requestsSpent
			s.putUsageLocked(id, l.tenantID, u)
		}
	}
	if b, ok := s.budgets[l.tenantID]; ok {
		b.ReservedAmount = max0(b.ReservedAmount - l.amount)
		if l.period == s.periods[l.tenantID] {
			b.SpentAmount += l.spent
		}
		b.UpdatedAt = s.nowFn()
		s.budgets[l.tenantID] = b
	}
	delete(s.leases, l.capID)
}

// Remaining is what l can still be charged.
func (l lease) Remaining() limes.Nanos {
	return l.amount - l.spent
}

// requestsRemaining is how many requests l can still be bumped.
func (l lease) requestsRemaining() int64 {
	return l.requests - l.requestsSpent
}

// coversCharges and coversBumps report whether l holds budget, or
// requests, for a charge or a bump to go under.
func (l lease) coversCharges() bool { return l.amount > 0 }
func (l lease) coversBumps() bool   { return l.requests > 0 }

// settleLeasesLocked settles every lease due says. s.mu must be held.
func (s *UsageStore) settleLeasesLocked(due func(lease) bool) int64 {
	var settled int64
	for _, l := range s.leases {
		if due(l) {
			s.settleLeaseLocked(l)
			settled++
		}
	}
	return settled
}

// leaseFor is the open lease a charge or a bump of capID with ancestors
// goes under: capID's own, or its nearest ancestor's, and how many of the
// ancestors are below the leaseholder; none when no capability on the
// chain holds one, or the nearest one does not cover the call. s.mu must
// be held.
func (s *UsageStore) leaseFor(capID uuid.UUID, ancestors []limes.Capability, covers func(lease) bool) (lease, int, bool) {
	now := s.nowFn()
	if l, ok := s.leases[capID]; ok && l.openAt(now) {
		return l, 0, covers(l)
	}
	for i, a := range ancestors {
		if l, ok := s.leases[a.ID]; ok && l.openAt(now) {
			return l, i, covers(l)
		}
	}
	return lease{}, 0, false
}

// bumpLeasedLocked counts one request of subject under l: on the
// capability and the ancestors below the leaseholder as a bump does, on
// the leaseholder and the lease's balance, and on nothing above it. below
// are the ancestors between the capability and the leaseholder. s.mu must
// be held.
func (s *UsageStore) bumpLeasedLocked(subject limes.MeterSubject, below []limes.Capability, l lease) (meter.BumpReceipt, error) {
	capID, tenantID := subject.CapabilityID(), subject.TenantID()
	held := capID == l.capID
	count := s.usage[capID].RequestCount
	// The leaseholder's own ceiling admitted the lease; a capability below
	// it is held to its own, and so is every ancestor between them.
	if !held {
		if err := s.admitRequestsLocked(subject, below, 1); err != nil {
			return meter.BumpReceipt{RequestCount: count}, err
		}
	}
	if l.requestsSpent >= l.requests {
		return meter.BumpReceipt{RequestCount: count}, fmt.Errorf("%w: %d of %d made", meter.ErrLeaseRequestsExhausted, l.requestsSpent, l.requests)
	}
	unit := subject.UnitCode()
	ids := append([]uuid.UUID{capID}, ancestorIDs(below)...)
	if !held {
		ids = append(ids, l.capID)
	}
	for _, id := range ids {
		u := s.usage[id]
		u.CapabilityID = id
		u.RequestCount++
		u.UnitCode = cmp.Or(u.UnitCode, unit)
		s.putUsageLocked(id, tenantID, u)
	}
	hu := s.usage[l.capID]
	hu.ReservedRequests = max(hu.ReservedRequests-1, 0)
	s.putUsageLocked(l.capID, tenantID, hu)
	l.requestsSpent++
	s.leases[l.capID] = l
	return meter.BumpReceipt{RequestCount: count + 1}, nil
}

// chargeLeasedLocked charges req under l: the capability and the ancestors
// below the leaseholder as a charge does, the leaseholder's counters and
// the lease's balance, and nothing above it — not the ancestors the lease
// is held against, not the tenant. below are the ancestors between the
// capability and the leaseholder; held reports whether the capability is
// the leaseholder itself. s.mu must be held.
func (s *UsageStore) chargeLeasedLocked(
	ctx context.Context, req meter.ChargeRequest, unit string, ancestors, below []limes.Capability, l lease,
	onCharged func(ctx context.Context, c meter.Charged) error,
) (meter.ChargeReceipt, error) {
	capID, tenantID := req.Subject.CapabilityID(), req.Subject.TenantID()
	held := capID == l.capID
	u := s.usage[capID]
	refused := meter.ChargeReceipt{SpentAmount: u.SpentAmount, TenantID: tenantID, UnitCode: unit}
	if unit != l.unit {
		return refused, fmt.Errorf("%w: a charge in %q under a lease in %q", limes.ErrUnitCodeMismatch, unit, l.unit)
	}
	refuse := func(err error) error { return err }
	check := ceilingCheck{
		amount: req.Amount, maxBudget: req.Subject.Budget(), unit: unit,
		capID: capID, tenantID: tenantID, ancestors: below, copies: req.Subject.Copies(),
	}
	if err := s.checkCopyCeilingsLocked(check, refuse); err != nil {
		return refused, err
	}
	// The leaseholder's own ceiling admitted the lease; a capability below
	// it is held to its own, and so is every ancestor between them.
	if !held {
		if err := s.checkCapabilityCeilingLocked(check, refuse); err != nil {
			return refused, err
		}
		if err := s.checkAncestorCeilingsLocked(check, refuse); err != nil {
			return refused, err
		}
	}
	if l.spent+req.Amount > l.amount {
		return refused, fmt.Errorf("%w: %s of %s left, %s asked", meter.ErrLeaseExhausted, l.Remaining(), l.amount, req.Amount)
	}
	receipt := meter.ChargeReceipt{ChargeID: uuid.New(), SpentAmount: u.SpentAmount + req.Amount, TenantID: tenantID, UnitCode: unit}
	if onCharged != nil {
		if err := onCharged(ctx, meter.Charged{Receipt: receipt}); err != nil {
			return refused, err
		}
	}
	for _, id := range append([]uuid.UUID{capID}, ancestorIDs(below)...) {
		cu := s.usage[id]
		cu.CapabilityID = id
		cu.SpentAmount += req.Amount
		cu.UnitCode = cmp.Or(cu.UnitCode, unit)
		s.putUsageLocked(id, tenantID, cu)
	}
	if !held {
		hu := s.usage[l.capID]
		hu.SpentAmount += req.Amount
		s.putUsageLocked(l.capID, tenantID, hu)
	}
	hu := s.usage[l.capID]
	hu.ReservedAmount = max0(hu.ReservedAmount - req.Amount)
	s.putUsageLocked(l.capID, tenantID, hu)
	l.spent += req.Amount
	s.leases[l.capID] = l
	for _, c := range req.Subject.Copies() {
		key := copyKeyOf(capID, c.RevocationID)
		cc := s.copies[key]
		cc.spent += req.Amount
		s.putCopyLocked(key, cc, ancestorIDs(ancestors))
	}
	s.appendLocked(ledgerEntry{
		ID: receipt.ChargeID, CapabilityID: capID, Ancestors: ancestorIDs(ancestors),
		TenantID: tenantID, Amount: req.Amount, UnitCode: unit,
		Op: req.Op, Actor: req.Actor, At: s.nowFn(), Copies: copyIDsOf(req.Subject.Copies()),
		ExternalRef: req.ExternalRef, tenantPeriod: l.period,
		leaseID: l.id, leaseHolder: l.capID,
	})
	return receipt, nil
}

// refundLeasedLocked gives amount of e, a charge taken under the lease
// still open, back to what it debited: the capability, the ancestors up to
// the leaseholder, the lease's balance and its copies. It reports false
// when the lease is no longer open, and the refund goes to the chain the
// settlement charged. s.mu must be held.
func (s *UsageStore) refundLeasedLocked(e *ledgerEntry, amount limes.Nanos) bool {
	l, ok := s.leases[e.leaseHolder]
	if e.leaseID == uuid.Nil() || !ok || l.id != e.leaseID {
		return false
	}
	ids := []uuid.UUID{e.CapabilityID}
	if e.CapabilityID != l.capID {
		ids = append(ids, e.Ancestors[:slices.Index(e.Ancestors, l.capID)+1]...)
	}
	for _, id := range ids {
		if u, ok := s.usage[id]; ok {
			u.SpentAmount = max0(u.SpentAmount - amount)
			if id == l.capID {
				u.ReservedAmount += amount
			}
			s.putUsageLocked(id, e.TenantID, u)
		}
	}
	l.spent = max0(l.spent - amount)
	s.leases[l.capID] = l
	for _, id := range e.Copies {
		key := copyKeyOf(e.CapabilityID, id)
		if cc, ok := s.copies[key]; ok {
			cc.spent = max0(cc.spent - amount)
			s.putCopyLocked(key, cc, nil)
		}
	}
	e.RefundedAmount += amount
	return true
}

var _ meter.Leaser = (*UsageStore)(nil)
