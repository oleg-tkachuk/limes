// Package memstore is an in-memory reference implementation of the limes
// storage contracts.
//
// It exists for three reasons, in order of importance:
//
//  1. It proves the contracts are satisfiable without a relational database
//     — the module's own suite runs against it with no database, no network
//     and no container.
//  2. It is the worked example a third party reads before writing their own
//     store. Everything here is deliberately obvious rather than fast.
//  3. Its Charge carries a staging-commit semantic, which is what makes the
//     atomicity guarantee demonstrable at all. Without one,
//     no implementation in the module could show that a failed side effect
//     leaves counters unmutated.
//
// It is NOT a production store: each store keeps its maps behind one mutex,
// which the usage store holds while onCharged runs, nothing survives a restart, and the charges
// ledger is kept whole for as long as the store lives.
package memstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/link"
)

// Store implements limes.Store — capability records and revocations.
//
// It is a separate type from UsageStore so that each reads as the contract it
// implements: records and revocations here, counters and the ledger there.
type Store struct {
	mu sync.Mutex

	caps     map[uuid.UUID]limes.Capability
	issuedBy map[uuid.UUID]limes.Principal
	revoked  map[uuid.UUID]limes.Revocation
	// revokedCopies maps a revoked Biscuit copy's revocation id to the
	// capability it belongs to.
	revokedCopies map[string]limes.BiscuitRevocation
	nowFn         func() time.Time
}

// UsageStore implements limes.UsageStore[TX] — request and spend counters.
//
// TX is the consumer's transaction handle. This store has no transactions of
// its own, so it never constructs one unless given a factory; it only threads
// the handle back to onCharged.
type UsageStore[TX any] struct {
	mu sync.Mutex

	usage   map[uuid.UUID]limes.Usage
	budgets map[uuid.UUID]limes.TenantBudget
	ledger  []ledgerEntry
	// The ledger's indexes, each to a position in ledger: by charge id, by
	// (capability, external ref) and by the reservation a charge settled.
	byCharge      map[uuid.UUID]int
	byRef         map[refKey]int
	byReservation map[uuid.UUID]int
	records       *Store // lineage and ceilings of ancestors
	holds         map[uuid.UUID]hold
	nowFn         func() time.Time
	txFactory     func() TX

	// copies counts each Biscuit copy that set limits of its own, by its
	// block's revocation id.
	copies map[string]copyCounter

	// periods counts each tenant's accounting periods: SetTenantBudget with
	// ResetSpend starts the next one. A ledger entry records the period it
	// was counted in, so a refund leaves a later period's spend alone.
	periods map[uuid.UUID]uint64
}

// refKey is what ChargeRequest.ExternalRef makes a charge idempotent on.
type refKey struct {
	capID uuid.UUID
	ref   string
}

// copyCounter is one Biscuit copy's own counters.
type copyCounter struct {
	capID    uuid.UUID // the capability the copy belongs to
	requests int64
	spent    limes.Nanos
	reserved limes.Nanos
}

// ledgerEntry records one committed charge.
type ledgerEntry struct {
	ID           uuid.UUID
	CapabilityID uuid.UUID
	// Ancestors are the capabilities the charge also debited, nearest
	// first, so a refund returns spend to exactly the same counters.
	Ancestors []uuid.UUID
	TenantID  uuid.UUID
	Amount    limes.Nanos
	UnitCode  string
	Op        limes.Op
	Actor     string
	At        time.Time
	// RefundedAmount is the total refunded from this charge so far.
	RefundedAmount limes.Nanos
	// Copies are the revocation ids of the Biscuit copies the charge also
	// debited, innermost first.
	Copies [][]byte
	// ExternalRef is the charge's ChargeRequest.ExternalRef; empty for none.
	ExternalRef string
	// ReservationID is the reservation the charge settled; uuid.Nil for a
	// charge made directly.
	ReservationID uuid.UUID
	// Overrun reports that the charge crossed a ceiling (OverrunRecord).
	Overrun bool

	// tenantPeriod is the tenant's accounting period the charge was counted
	// in (UsageStore.periods).
	tenantPeriod uint64
}

// StoreConfig configures a Store.
type StoreConfig struct {
	// Now is the clock. Nil: time.Now.
	Now func() time.Time
}

// NewStore returns an empty record store.
func NewStore(cfg StoreConfig) (*Store, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Store{
		caps:     map[uuid.UUID]limes.Capability{},
		issuedBy: map[uuid.UUID]limes.Principal{},
		revoked:  map[uuid.UUID]limes.Revocation{},
		nowFn:    cfg.Now,

		revokedCopies: map[string]limes.BiscuitRevocation{},
	}, nil
}

// UsageStoreConfig configures a UsageStore. TX is the consumer's
// transaction handle, threaded back to onCharged.
type UsageStoreConfig[TX any] struct {
	// Records is how the usage store finds each capability's ancestors and
	// their ceilings, and which capabilities still exist for PurgeOrphans.
	// Required: without it no ancestor's ceiling would bind, which a store
	// over a database never does.
	Records *Store
	// Now is the clock. Nil: time.Now.
	Now func() time.Time
	// TxFactory makes the handle passed to onCharged. Nil: the zero TX.
	TxFactory func() TX
}

// NewUsageStore returns an empty usage store.
//
//	memstore.NewUsageStore(memstore.UsageStoreConfig[struct{}]{Records: records}) // no transactions
//	memstore.NewUsageStore(memstore.UsageStoreConfig[pgx.Tx]{Records: records, TxFactory: begin}) // a real handle
func NewUsageStore[TX any](cfg UsageStoreConfig[TX]) (*UsageStore[TX], error) {
	if cfg.Records == nil {
		return nil, errors.New("memstore: UsageStoreConfig.Records required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &UsageStore[TX]{
		usage:   map[uuid.UUID]limes.Usage{},
		budgets: map[uuid.UUID]limes.TenantBudget{},
		holds:   map[uuid.UUID]hold{},
		copies:  map[string]copyCounter{},
		periods: map[uuid.UUID]uint64{},

		byCharge:      map[uuid.UUID]int{},
		byRef:         map[refKey]int{},
		byReservation: map[uuid.UUID]int{},
		records:       cfg.Records,
		nowFn:         cfg.Now,
		txFactory:     cfg.TxFactory,
	}, nil
}

// ─── limes.Store ──────────────────────────────────────────────────────

// issuedBy is recorded alongside the capability so the in-memory store answers
// the same attribution questions the relational one does — a test double that
// silently drops the initiator would let an unattributed issuance pass.
func (s *Store) Insert(_ context.Context, req limes.InsertRequest) error {
	c, issuedBy := req.Capability, req.IssuedBy
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.caps[c.ID]; ok {
		return fmt.Errorf("%w: %s", limes.ErrAlreadyExists, c.ID)
	}
	// Copies in and out: the caller keeps its slices, and a record never
	// changes after it is written.
	s.caps[c.ID] = c.Clone()
	s.issuedBy[c.ID] = issuedBy.Clone()
	return nil
}

// GetRecord returns the capability, who issued it and its own revocation.
func (s *Store) GetRecord(_ context.Context, id uuid.UUID) (limes.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.caps[id]
	if !ok {
		return limes.Record{}, limes.ErrNotFound
	}
	rec := limes.Record{Capability: c.Clone(), IssuedBy: s.issuedBy[id].Clone()}
	if r, ok := s.revoked[id]; ok {
		rec.Revocation = &r
	}
	return rec, nil
}

// revokeLocked writes id's revocation entry unless it has one: the first
// entry stands, as a repeated revoke is a no-op. s.mu must be held.
func (s *Store) revokeLocked(id uuid.UUID, r limes.Revocation) {
	if _, ok := s.revoked[id]; !ok {
		s.revoked[id] = r
	}
}

// isRevokedItselfLocked reports whether id has a revocation entry of its own.
// s.mu must be held.
func (s *Store) isRevokedItselfLocked(id uuid.UUID) bool {
	_, ok := s.revoked[id]
	return ok
}

// IsRevoked answers for the whole delegation chain: a capability is revoked
// when it, or any ancestor still on record, is.
func (s *Store) IsRevoked(_ context.Context, id uuid.UUID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.revoked[id]; ok {
		return true, nil
	}
	ancestors, err := s.ancestorsLocked(id)
	if err != nil {
		return false, err
	}
	for _, a := range ancestors {
		if _, ok := s.revoked[a.ID]; ok {
			return true, nil
		}
	}
	return false, nil
}

// lineageOf returns the records of id's ancestors, nearest first, stopping
// at the first one no longer on record, and ErrNotFound when id itself is not
// on record. A chain with more than limes.MaxDelegationDepth of them still on
// record — or a corrupt cycle — is limes.ErrDelegationTooDeep, so a walk that
// stops short never answers for an ancestor it did not reach.
func (s *Store) lineageOf(id uuid.UUID) ([]limes.Capability, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.caps[id]; !ok {
		return nil, fmt.Errorf("%w: memstore: capability %s is not on record", limes.ErrNotFound, id)
	}
	return s.ancestorsLocked(id)
}

func (s *Store) ancestorsLocked(id uuid.UUID) ([]limes.Capability, error) {
	var out []limes.Capability
	c, ok := s.caps[id]
	for ok && c.ParentID != uuid.Nil {
		if len(out) == limes.MaxDelegationDepth {
			if _, more := s.caps[c.ParentID]; more {
				return nil, fmt.Errorf("%w: %s has more than %d ancestors", limes.ErrDelegationTooDeep, id, limes.MaxDelegationDepth)
			}
			break
		}
		c, ok = s.caps[c.ParentID]
		if ok {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *Store) exists(id uuid.UUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.caps[id]
	return ok
}

// Revoke is idempotent. CascadeChildren walks the delegation tree by ParentID
// so revoking an orchestrator takes the sub-agents it spawned with it.
func (s *Store) Revoke(_ context.Context, args limes.RevokeRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.caps[args.CapabilityID]; !ok {
		return limes.ErrNotFound
	}
	entry := limes.Revocation{
		RevokedAt: s.nowFn(), Note: args.Note, Actor: args.Actor, Cascaded: args.CascadeChildren,
	}
	s.revokeLocked(args.CapabilityID, entry)
	if !args.CascadeChildren {
		return nil
	}
	// Breadth-first down the delegation tree from args.ID alone, to the
	// depth IsRevoked walks: a capability revoked earlier without
	// cascade elsewhere in the store is not a root of this walk.
	level := []uuid.UUID{args.CapabilityID}
	for depth := 0; len(level) > 0 && depth < limes.MaxDelegationDepth; depth++ {
		var next []uuid.UUID
		for id, c := range s.caps {
			if slices.Contains(level, c.ParentID) {
				s.revokeLocked(id, entry)
				next = append(next, id)
			}
		}
		level = next
	}
	return nil
}

// IsBiscuitRevoked implements limes.BiscuitRevocationLookup.
func (s *Store) IsBiscuitRevoked(_ context.Context, revocationIDs [][]byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range revocationIDs {
		if _, ok := s.revokedCopies[string(id)]; ok {
			return true, nil
		}
	}
	return false, nil
}

// RevokeBiscuit implements limes.BiscuitRevocationStore.
func (s *Store) RevokeBiscuit(_ context.Context, args limes.RevokeBiscuitRequest) error {
	if err := args.Validate(); err != nil {
		return err
	}
	capID, revocationID := args.Copy.CapabilityID(), args.Copy.RevocationID()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.caps[capID]; !ok {
		return limes.ErrNotFound
	}
	if _, ok := s.revokedCopies[string(revocationID)]; !ok { // the first entry stands
		s.revokedCopies[string(revocationID)] = limes.BiscuitRevocation{
			CapabilityID: capID, RevocationID: revocationID,
			RevokedAt: s.nowFn(), Note: args.Note, Actor: args.Actor,
		}
	}
	return nil
}

// GetBiscuitRevocation returns what RevokeBiscuit wrote for revocationID.
func (s *Store) GetBiscuitRevocation(_ context.Context, revocationID []byte) (limes.BiscuitRevocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.revokedCopies[string(revocationID)]
	if !ok {
		return limes.BiscuitRevocation{}, limes.ErrNotFound
	}
	r.RevocationID = slices.Clone(r.RevocationID)
	return r, nil
}

// Purge implements limes.Store, oldest expiry first.
func (s *Store) Purge(_ context.Context, req limes.PurgeRequest) (limes.PurgeResult, error) {
	if err := req.Validate(); err != nil {
		return limes.PurgeResult{}, err
	}
	limit := req.Limit
	if limit == 0 {
		limit = limes.DefaultHousekeepingLimit
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := s.nowFn().Add(-req.Retention())
	var due []limes.Capability
	for _, c := range s.caps {
		if !c.ExpiresAt.IsZero() && c.ExpiresAt.Before(cutoff) {
			due = append(due, c)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].ExpiresAt.Before(due[j].ExpiresAt) })
	if len(due) > limit {
		due = due[:limit]
	}
	for _, c := range due {
		delete(s.caps, c.ID)
		delete(s.revoked, c.ID)
		delete(s.issuedBy, c.ID)
		maps.DeleteFunc(s.revokedCopies, func(_ string, r limes.BiscuitRevocation) bool { return r.CapabilityID == c.ID })
	}
	return limes.PurgeResult{Purged: int64(len(due))}, nil
}

func (s *Store) ListByPrincipal(_ context.Context, req limes.ListByPrincipalRequest) (limes.Page[limes.Capability], error) {
	if err := req.Validate(); err != nil {
		return limes.Page[limes.Capability]{}, err
	}
	var after uuid.UUID
	if req.Cursor != "" {
		var err error
		if after, err = uuid.Parse(req.Cursor); err != nil {
			return limes.Page[limes.Capability]{}, fmt.Errorf("%w: memstore: cursor %q", limes.ErrInvalidRequest, req.Cursor)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.nowFn()
	out := make([]limes.Capability, 0, len(s.caps))
	for id, c := range s.caps {
		switch {
		case c.Subject.TenantID != req.TenantID, c.Subject.Type != req.PrincipalType, c.Subject.Subject != req.Subject:
			continue
		case !req.IncludeExpired && !c.ExpiresAt.After(now):
			continue
		case !req.IncludeRevoked && s.isRevokedItselfLocked(id):
			continue
		case req.Cursor != "" && !uuidLess(after, id):
			continue
		}
		out = append(out, c.Clone())
	}
	// Ascending id, the order the cursor seeks in.
	sort.Slice(out, func(i, j int) bool { return uuidLess(out[i].ID, out[j].ID) })
	limit := int(limes.PageLimit(req.Limit))
	if len(out) <= limit {
		return limes.Page[limes.Capability]{Items: out}, nil
	}
	out = out[:limit]
	return limes.Page[limes.Capability]{Items: out, Next: out[len(out)-1].ID.String()}, nil
}

// percent turns a fraction into a percentage.
const percent = 100

// uuidLess is the ascending id order Store.ListByPrincipal pages in:
// bytewise, as UUIDs compare.
func uuidLess(a, b uuid.UUID) bool { return bytes.Compare(a[:], b[:]) < 0 }

// ─── limes.UsageStore[TX] ─────────────────────────────────────────────

// checkSubject refuses a subject no verifier or record made: the zero
// MeterSubject names no capability and no tenant to count against.
func checkSubject(s limes.MeterSubject) error {
	if s.CapabilityID() == uuid.Nil || s.TenantID() == uuid.Nil {
		return fmt.Errorf("%w: memstore: the subject names no capability or no tenant", limes.ErrInvalidRequest)
	}
	return nil
}

// lineage returns the ancestors whose counters move with capID's, and
// ErrNotFound when capID itself is not on record: with no record its
// ancestors cannot be read, and metering it alone would let it spend past
// every live ancestor's ceiling.
func (s *UsageStore[TX]) lineage(capID uuid.UUID) ([]limes.Capability, error) {
	return s.records.lineageOf(capID)
}

// Bump returns ErrRequestLimitExceeded WITHOUT mutating when the
// increment would cross the capability's ceiling or any ancestor's. A
// rejected call must be safe to retry.
func (s *UsageStore[TX]) Bump(_ context.Context, req limes.BumpRequest) (limes.BumpReceipt, error) {
	if err := checkSubject(req.Subject); err != nil {
		return limes.BumpReceipt{}, err
	}
	// The lineage is read under the usage lock, as DeleteUsage and PurgeOrphans
	// take it: read before, an ancestor they dropped meanwhile would have
	// its row written back.
	s.mu.Lock()
	defer s.mu.Unlock()
	ancestors, err := s.lineage(req.Subject.CapabilityID())
	if err != nil {
		return limes.BumpReceipt{}, err
	}
	u := s.usage[req.Subject.CapabilityID()]
	next := u.RequestCount + 1
	for _, c := range req.Subject.Copies() {
		if !c.Requests.Admits(s.copies[string(c.RevocationID)].requests + 1) {
			return limes.BumpReceipt{RequestCount: u.RequestCount}, fmt.Errorf("%w: copy %x", limes.ErrRequestLimitExceeded, c.RevocationID)
		}
	}
	if !req.Subject.Requests().Admits(next) {
		return limes.BumpReceipt{RequestCount: u.RequestCount}, limes.ErrRequestLimitExceeded
	}
	for _, a := range ancestors {
		if !a.Caveats.Requests.Admits(s.usage[a.ID].RequestCount + 1) {
			return limes.BumpReceipt{RequestCount: u.RequestCount}, fmt.Errorf("%w: ancestor %s", limes.ErrRequestLimitExceeded, a.ID)
		}
	}
	for _, a := range ancestors {
		au := s.usage[a.ID]
		au.CapabilityID = a.ID
		au.RequestCount++
		s.usage[a.ID] = au
	}
	for _, c := range req.Subject.Copies() {
		cc := s.copies[string(c.RevocationID)]
		cc.capID = req.Subject.CapabilityID()
		cc.requests++
		s.copies[string(c.RevocationID)] = cc
	}
	u.CapabilityID = req.Subject.CapabilityID()
	u.RequestCount = next
	s.usage[req.Subject.CapabilityID()] = u
	return limes.BumpReceipt{RequestCount: next}, nil
}

// hold is one open reservation and every counter it holds against.
type hold struct {
	id        uuid.UUID
	capID     uuid.UUID
	ancestors []uuid.UUID
	tenantID  uuid.UUID
	amount    limes.Nanos
	unit      string
	op        limes.Op
	actor     string
	expires   time.Time
	copies    []limes.CopyCeiling
}

// Charge applies the ceiling chain and commits atomically.
//
// The staging semantic is the point of this method: every mutation is computed
// into locals, onCharged runs, and only if it succeeds are the counters and the
// ledger published. A rejection by any ceiling, or an error from onCharged,
// therefore leaves EVERY counter and the ledger exactly as they were — which
// is the atomicity guarantee.
func (s *UsageStore[TX]) Charge(
	ctx context.Context,
	req limes.ChargeRequest,
	onCharged func(ctx context.Context, tx TX) error,
) (limes.ChargeReceipt, error) {
	if err := req.Amount.Validate(); err != nil {
		return limes.ChargeReceipt{}, err
	}
	if err := req.Overrun.Validate(); err != nil {
		return limes.ChargeReceipt{}, err
	}
	if err := limes.ValidateExternalRef(req.ExternalRef); err != nil {
		return limes.ChargeReceipt{}, err
	}
	if err := checkSubject(req.Subject); err != nil {
		return limes.ChargeReceipt{}, err
	}
	unit, err := limes.NormalizeUnitCode(req.Subject.UnitCode())
	if err != nil {
		return limes.ChargeReceipt{}, err
	}
	// The lineage is read under the usage lock, as DeleteUsage and PurgeOrphans
	// take it: read before, an ancestor they dropped meanwhile would have
	// its row written back.
	s.mu.Lock()
	defer s.mu.Unlock()
	ancestors, err := s.lineage(req.Subject.CapabilityID())
	if err != nil {
		return limes.ChargeReceipt{}, err
	}
	if req.ExternalRef != "" {
		if e, ok := entryLocked(s, s.byRef, refKey{req.Subject.CapabilityID(), req.ExternalRef}); ok {
			if e.Amount != req.Amount || e.UnitCode != unit || e.TenantID != req.Subject.TenantID() {
				return limes.ChargeReceipt{}, fmt.Errorf("%w: %q was %s %s for tenant %s",
					limes.ErrExternalRefConflict, req.ExternalRef, e.Amount, e.UnitCode, e.TenantID)
			}
			return s.replayLocked(e), nil
		}
	}
	return s.chargeLocked(ctx, req, unit, ancestorIDs(ancestors), ancestors, nil, onCharged)
}

// ChargeByRef returns the charge a capability took under externalRef.
func (s *UsageStore[TX]) ChargeByRef(_ context.Context, req limes.ChargeByRefRequest) (limes.ChargeRecord, error) {
	capID, externalRef := req.CapabilityID, req.ExternalRef
	if externalRef == "" {
		return limes.ChargeRecord{}, limes.ErrChargeNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := entryLocked(s, s.byRef, refKey{capID, externalRef})
	if !ok {
		return limes.ChargeRecord{}, limes.ErrChargeNotFound
	}
	return e.record(), nil
}

// GetCharge returns one charge of the ledger by its id.
func (s *UsageStore[TX]) GetCharge(_ context.Context, chargeID uuid.UUID) (limes.ChargeRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := entryLocked(s, s.byCharge, chargeID)
	if !ok {
		return limes.ChargeRecord{}, limes.ErrChargeNotFound
	}
	return e.record(), nil
}

// record is the entry as the Meter contract reads a charge back.
func (e ledgerEntry) record() limes.ChargeRecord {
	return limes.ChargeRecord{
		ID: e.ID, CapabilityID: e.CapabilityID, TenantID: e.TenantID, Amount: e.Amount, RefundedAmount: e.RefundedAmount,
		UnitCode: e.UnitCode, ExternalRef: e.ExternalRef, Overrun: e.Overrun, Op: e.Op, Actor: e.Actor, At: e.At,
	}
}

// entryLocked returns the ledger entry index files under key. s.mu must be
// held.
func entryLocked[TX any, K comparable](s *UsageStore[TX], index map[K]int, key K) (ledgerEntry, bool) {
	i, ok := index[key]
	if !ok {
		return ledgerEntry{}, false
	}
	return s.ledger[i], true
}

// appendLocked adds entry to the ledger and its indexes. s.mu must be held.
func (s *UsageStore[TX]) appendLocked(entry ledgerEntry) {
	i := len(s.ledger)
	s.ledger = append(s.ledger, entry)
	s.byCharge[entry.ID] = i
	if entry.ExternalRef != "" {
		s.byRef[refKey{entry.CapabilityID, entry.ExternalRef}] = i
	}
	if entry.ReservationID != uuid.Nil {
		s.byReservation[entry.ReservationID] = i
	}
}

// replayLocked is the receipt for a charge already in the ledger, returned
// when the same charge arrives again. s.mu must be held.
func (s *UsageStore[TX]) replayLocked(e ledgerEntry) limes.ChargeReceipt {
	return limes.ChargeReceipt{
		ChargeID: e.ID, SpentAmount: s.usage[e.CapabilityID].SpentAmount,
		Replayed: true, Overrun: e.Overrun, TenantID: e.TenantID, UnitCode: e.UnitCode,
	}
}

func ancestorIDs(ancestors []limes.Capability) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(ancestors))
	for _, a := range ancestors {
		ids = append(ids, a.ID)
	}
	return ids
}

// chargeLocked checks every ceiling and, if all admit the charge, publishes
// it together with the release of settling (when non-nil). Held amounts count
// against each ceiling, except the one being settled. s.mu must be held.
func (s *UsageStore[TX]) chargeLocked(
	ctx context.Context,
	req limes.ChargeRequest,
	unit string,
	ids []uuid.UUID,
	ancestors []limes.Capability,
	settling *hold,
	onCharged func(ctx context.Context, tx TX) error,
) (limes.ChargeReceipt, error) {
	var released limes.Nanos
	reservationID := uuid.Nil
	if settling != nil {
		released = settling.amount
		reservationID = settling.id
	}
	// crossed decides a ceiling the charge would cross: under OverrunRecord
	// the charge goes on and the receipt says so; otherwise it is refused
	// with err, and nothing has been published.
	overrun := false
	crossed := func(err error) error {
		if req.Overrun == limes.OverrunRecord {
			overrun = true
			return nil
		}
		return err
	}

	u := s.usage[req.Subject.CapabilityID()]
	if err := s.checkCeilingsLocked(ceilingCheck{
		amount: req.Amount, released: released, maxBudget: req.Subject.Budget(), unit: unit,
		capID: req.Subject.CapabilityID(), tenantID: req.Subject.TenantID(), ancestors: ancestors, copies: req.Subject.Copies(),
	}, crossed); err != nil {
		return limes.ChargeReceipt{SpentAmount: u.SpentAmount, TenantID: req.Subject.TenantID(), UnitCode: unit}, err
	}
	stagedSpent := u.SpentAmount + req.Amount

	// the caller's side effect, on the caller's handle, before anything is
	// published
	if onCharged != nil {
		var tx TX
		if s.txFactory != nil {
			tx = s.txFactory()
		}
		if err := onCharged(ctx, tx); err != nil {
			// Nothing has been published — nothing to compensate.
			return limes.ChargeReceipt{SpentAmount: u.SpentAmount, TenantID: req.Subject.TenantID(), UnitCode: unit}, err
		}
	}

	// ── commit: publish every staged mutation together ──
	u.CapabilityID = req.Subject.CapabilityID()
	u.SpentAmount = stagedSpent
	u.ReservedAmount = max0(u.ReservedAmount - released)
	u.UnitCode = unit
	s.usage[req.Subject.CapabilityID()] = u
	for _, id := range ids {
		au := s.usage[id]
		au.CapabilityID = id
		au.SpentAmount += req.Amount
		au.ReservedAmount = max0(au.ReservedAmount - released)
		if au.UnitCode == "" {
			au.UnitCode = unit
		}
		s.usage[id] = au
	}
	b := s.tenantLocked(req.Subject.TenantID(), unit)
	b.SpentAmount += req.Amount
	b.ReservedAmount = max0(b.ReservedAmount - released)
	b.UpdatedAt = s.nowFn()
	s.budgets[req.Subject.TenantID()] = b
	copies := req.Subject.Copies()
	copyIDs := make([][]byte, 0, len(copies))
	for _, c := range copies {
		cc := s.copies[string(c.RevocationID)]
		cc.capID = req.Subject.CapabilityID()
		cc.spent += req.Amount
		cc.reserved = max0(cc.reserved - released)
		s.copies[string(c.RevocationID)] = cc
		copyIDs = append(copyIDs, c.RevocationID)
	}
	entry := ledgerEntry{
		ID: uuid.New(), CapabilityID: req.Subject.CapabilityID(), Ancestors: ids,
		TenantID: req.Subject.TenantID(), Amount: req.Amount, UnitCode: unit,
		Op: req.Op, Actor: req.Actor, At: s.nowFn(), Copies: copyIDs,
		ExternalRef: req.ExternalRef, ReservationID: reservationID, Overrun: overrun,
		tenantPeriod: s.periods[req.Subject.TenantID()],
	}
	s.appendLocked(entry)
	return limes.ChargeReceipt{
		ChargeID: entry.ID, SpentAmount: stagedSpent, Overrun: overrun, TenantID: req.Subject.TenantID(), UnitCode: unit,
	}, nil
}

// tenantLocked returns the tenant's aggregate row, or a new one without a
// ceiling. The aggregate counts from the first charge or hold, not from the
// first SetTenantBudget, so a ceiling set later is measured against what the
// tenant has already spent and holds. s.mu must be held.
func (s *UsageStore[TX]) tenantLocked(tenantID uuid.UUID, unit string) limes.TenantBudget {
	if b, ok := s.budgets[tenantID]; ok {
		return b
	}
	// A ceiling never set lifts nothing and limits nothing: the row only
	// counts, until SetTenantBudget gives it a ceiling.
	return limes.TenantBudget{
		TenantID: tenantID, Ceiling: limes.BudgetCeiling{Unlimited: true}, UnitCode: unit, PeriodStart: s.nowFn(),
	}
}

// ceilingCheck is an amount measured against every ceiling. released is
// the amount of a hold being settled by it, which no longer counts.
type ceilingCheck struct {
	amount, released limes.Nanos
	maxBudget        limes.BudgetCeiling // the capability's own, from its token
	unit             string              // normalised
	capID, tenantID  uuid.UUID
	ancestors        []limes.Capability
	copies           []limes.CopyCeiling
}

// checkCeilingsLocked measures c against the copy's ceilings innermost
// first, then the capability's, each ancestor's and the tenant aggregate's,
// with whatever is already spent and held, when c adds to it. Each ceiling c would cross goes
// to crossed, which returns the error to stop on or nil to carry on.
// s.mu must be held.
func (s *UsageStore[TX]) checkCeilingsLocked(c ceilingCheck, crossed func(error) error) error {
	if err := s.checkUnitsLocked(c); err != nil {
		return err
	}
	if err := s.checkRangeLocked(c); err != nil {
		return err
	}
	// A settle at or below its hold adds nothing to what every counter
	// already holds, so no ceiling can refuse it — not even one crossed
	// since the hold was made. Only the excess must fit.
	if c.amount <= c.released {
		return nil
	}
	for _, cp := range c.copies {
		cc := s.copies[string(cp.RevocationID)]
		if !cp.Budget.Admits(cc.spent + cc.reserved - c.released + c.amount) {
			if err := crossed(fmt.Errorf("%w: copy %x", limes.ErrBudgetExceeded, cp.RevocationID)); err != nil {
				return err
			}
		}
	}
	u := s.usage[c.capID]
	if !c.maxBudget.Admits(u.SpentAmount + u.ReservedAmount - c.released + c.amount) {
		if err := crossed(limes.ErrBudgetExceeded); err != nil {
			return err
		}
	}
	for _, a := range c.ancestors {
		au := s.usage[a.ID]
		if !a.Caveats.Budget.Admits(au.SpentAmount + au.ReservedAmount - c.released + c.amount) {
			if err := crossed(fmt.Errorf("%w: ancestor %s", limes.ErrBudgetExceeded, a.ID)); err != nil {
				return err
			}
		}
	}
	if b, ok := s.budgets[c.tenantID]; ok &&
		!b.Ceiling.Admits(b.SpentAmount+c.amount+max0(b.ReservedAmount-c.released)) {
		if err := crossed(limes.ErrTenantBudgetExceeded); err != nil {
			return err
		}
	}
	return nil
}

// checkUnitsLocked refuses an amount in a unit other than one a counter it
// would move already counts in. Like the range, it is not a ceiling an
// overrun may cross: the sum would mean nothing. s.mu must be held.
func (s *UsageStore[TX]) checkUnitsLocked(c ceilingCheck) error {
	differs := func(have string) bool { return have != "" && have != c.unit }
	if differs(s.usage[c.capID].UnitCode) {
		return fmt.Errorf("%w: capability %s counts in %s, not %s",
			limes.ErrUnitCodeMismatch, c.capID, s.usage[c.capID].UnitCode, c.unit)
	}
	for _, a := range c.ancestors {
		if have := s.usage[a.ID].UnitCode; differs(have) {
			return fmt.Errorf("%w: ancestor %s counts in %s, not %s", limes.ErrUnitCodeMismatch, a.ID, have, c.unit)
		}
	}
	if b, ok := s.budgets[c.tenantID]; ok && differs(b.UnitCode) {
		return fmt.Errorf("%w: tenant %s counts in %s, not %s", limes.ErrUnitCodeMismatch, c.tenantID, b.UnitCode, c.unit)
	}
	return nil
}

// checkRangeLocked keeps every counter c would move within MaxNanos, the
// range a store holds. Unlike a ceiling it is not an overrun to record: past
// it the amount could not be counted at all. s.mu must be held.
func (s *UsageStore[TX]) checkRangeLocked(c ceilingCheck) error {
	fits := func(spent, reserved limes.Nanos) bool {
		return spent+max0(reserved-c.released) <= limes.MaxNanos-c.amount
	}
	ok := true
	for _, cp := range c.copies {
		cc := s.copies[string(cp.RevocationID)]
		ok = ok && fits(cc.spent, cc.reserved)
	}
	u := s.usage[c.capID]
	ok = ok && fits(u.SpentAmount, u.ReservedAmount)
	for _, a := range c.ancestors {
		au := s.usage[a.ID]
		ok = ok && fits(au.SpentAmount, au.ReservedAmount)
	}
	if b, found := s.budgets[c.tenantID]; found {
		ok = ok && fits(b.SpentAmount, b.ReservedAmount)
	}
	if !ok {
		return fmt.Errorf("%w: %s more", limes.ErrCounterOverflow, c.amount)
	}
	return nil
}

// Reserve holds an amount against every ceiling Charge checks.
func (s *UsageStore[TX]) Reserve(_ context.Context, req limes.ReserveRequest) (limes.Reservation, error) {
	if err := req.Amount.Validate(); err != nil {
		return limes.Reservation{}, err
	}
	if err := checkSubject(req.Subject); err != nil {
		return limes.Reservation{}, err
	}
	unit, err := limes.NormalizeUnitCode(req.Subject.UnitCode())
	if err != nil {
		return limes.Reservation{}, err
	}
	ttl := req.TTL
	if ttl < 0 {
		return limes.Reservation{}, fmt.Errorf("%w: memstore: reservation TTL %s is negative", limes.ErrInvalidRequest, ttl)
	}
	if ttl == 0 {
		ttl = limes.DefaultReservationTTL
	}
	// The lineage is read under the usage lock, as DeleteUsage and PurgeOrphans
	// take it: read before, an ancestor they dropped meanwhile would have
	// its row written back.
	s.mu.Lock()
	defer s.mu.Unlock()
	ancestors, err := s.lineage(req.Subject.CapabilityID())
	if err != nil {
		return limes.Reservation{}, err
	}

	refuse := func(err error) error { return err }
	if err := s.checkCeilingsLocked(ceilingCheck{
		amount: req.Amount, maxBudget: req.Subject.Budget(), unit: unit,
		capID: req.Subject.CapabilityID(), tenantID: req.Subject.TenantID(), ancestors: ancestors, copies: req.Subject.Copies(),
	}, refuse); err != nil {
		return limes.Reservation{}, err
	}

	h := hold{
		id:    uuid.New(),
		capID: req.Subject.CapabilityID(), ancestors: ancestorIDs(ancestors), tenantID: req.Subject.TenantID(),
		amount: req.Amount, unit: unit, op: req.Op, actor: req.Actor,
		expires: s.nowFn().Add(ttl), copies: req.Subject.Copies(),
	}
	s.applyHoldLocked(h, +1)
	s.holds[h.id] = h
	return h.reservation(), nil
}

// applyHoldLocked adds (sign +1) or removes (sign -1) a hold on every counter.
func (s *UsageStore[TX]) applyHoldLocked(h hold, sign limes.Nanos) {
	for _, id := range append([]uuid.UUID{h.capID}, h.ancestors...) {
		u := s.usage[id]
		u.CapabilityID = id
		u.ReservedAmount = max0(u.ReservedAmount + sign*h.amount)
		if u.UnitCode == "" {
			u.UnitCode = h.unit
		}
		s.usage[id] = u
	}
	b := s.tenantLocked(h.tenantID, h.unit)
	b.ReservedAmount = max0(b.ReservedAmount + sign*h.amount)
	s.budgets[h.tenantID] = b
	for _, c := range h.copies {
		cc := s.copies[string(c.RevocationID)]
		cc.capID = h.capID
		cc.reserved = max0(cc.reserved + sign*h.amount)
		s.copies[string(c.RevocationID)] = cc
	}
}

// Settle charges the actual cost in place of a reservation.
func (s *UsageStore[TX]) Settle(
	ctx context.Context,
	req limes.SettleRequest,
	onCharged func(ctx context.Context, tx TX) error,
) (limes.ChargeReceipt, error) {
	if err := req.Amount.Validate(); err != nil {
		return limes.ChargeReceipt{}, err
	}
	if err := req.Overrun.Validate(); err != nil {
		return limes.ChargeReceipt{}, err
	}
	// A direct charge is filed under the zero reservation id, so the zero id
	// must not reach the replay lookup below.
	if req.ReservationID == uuid.Nil {
		return limes.ChargeReceipt{}, limes.ErrReservationNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	subject := req.Subject.CapabilityID()
	h, ok := s.holds[req.ReservationID]
	if !ok {
		// A reservation of another capability is none of this one's.
		if e, settled := entryLocked(s, s.byReservation, req.ReservationID); settled && e.CapabilityID == subject {
			return s.replayLocked(e), nil
		}
	}
	if !ok || h.capID != subject || !h.expires.After(s.nowFn()) {
		return limes.ChargeReceipt{}, limes.ErrReservationNotFound
	}
	ancestors, err := s.lineage(h.capID)
	if err != nil {
		return limes.ChargeReceipt{}, err
	}
	// The charge is the reservation's: its tenant, unit and the copy limits
	// it was made under, against the subject's own ceiling.
	held := link.NewMeterSubject(link.MeterSubjectFields{
		CapabilityID: h.capID, TenantID: h.tenantID, Requests: req.Subject.Requests(),
		Budget: req.Subject.Budget(), UnitCode: h.unit, Copies: h.copies,
	}).(limes.MeterSubject)
	receipt, err := s.chargeLocked(ctx, limes.ChargeRequest{
		Subject: held, Amount: req.Amount, Op: h.op, Actor: h.actor, Overrun: req.Overrun,
	}, h.unit, h.ancestors, ancestors, &h, onCharged)
	if err != nil {
		return receipt, err
	}
	delete(s.holds, req.ReservationID)
	return receipt, nil
}

// reservation is the hold as the Meter contract reads it back.
func (h hold) reservation() limes.Reservation {
	return limes.Reservation{
		ID: h.id, CapabilityID: h.capID, TenantID: h.tenantID, Amount: h.amount, UnitCode: h.unit,
		Op: h.op, Actor: h.actor, Copies: budgetsOnly(h.copies), ExpiresAt: h.expires,
	}
}

// budgetsOnly is copies with their budgets alone, as a hold keeps them.
func budgetsOnly(copies []limes.CopyCeiling) []limes.CopyCeiling {
	if len(copies) == 0 {
		return nil
	}
	out := make([]limes.CopyCeiling, len(copies))
	for i, c := range copies {
		out[i] = limes.CopyCeiling{
			RevocationID: slices.Clone(c.RevocationID), Budget: c.Budget,
			Requests: limes.RequestCeiling{Unlimited: true}, // a hold counts no requests
		}
	}
	return out
}

// GetReservation returns a hold that still counts, expired or not.
func (s *UsageStore[TX]) GetReservation(_ context.Context, reservationID uuid.UUID) (limes.Reservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.holds[reservationID]
	if !ok {
		return limes.Reservation{}, limes.ErrReservationNotFound
	}
	return h.reservation(), nil
}

// ListReservations returns the capability's own holds, soonest to expire first.
func (s *UsageStore[TX]) ListReservations(_ context.Context, req limes.ListReservationsRequest) (limes.Page[limes.Reservation], error) {
	if err := req.Validate(); err != nil {
		return limes.Page[limes.Reservation]{}, err
	}
	var after *reservationPlace
	if req.Cursor != "" {
		p, err := parseReservationPlace(req.Cursor)
		if err != nil {
			return limes.Page[limes.Reservation]{}, err
		}
		after = &p
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []limes.Reservation{}
	for _, h := range s.holds {
		if h.capID == req.CapabilityID && (after == nil || after.before(placeOf(h.reservation()))) {
			out = append(out, h.reservation())
		}
	}
	// Soonest to expire first, ties by id.
	sort.Slice(out, func(i, j int) bool { return placeOf(out[i]).before(placeOf(out[j])) })
	limit := int(limes.PageLimit(req.Limit))
	if len(out) <= limit {
		return limes.Page[limes.Reservation]{Items: out}, nil
	}
	out = out[:limit]
	return limes.Page[limes.Reservation]{Items: out, Next: placeOf(out[len(out)-1]).String()}, nil
}

// reservationPlace is a reservation's place in ListReservations' order, and
// memstore's cursor: its expiry and id.
type reservationPlace struct {
	expires time.Time
	id      uuid.UUID
}

func placeOf(r limes.Reservation) reservationPlace { return reservationPlace{r.ExpiresAt, r.ID} }

func (p reservationPlace) before(q reservationPlace) bool {
	if !p.expires.Equal(q.expires) {
		return p.expires.Before(q.expires)
	}
	return uuidLess(p.id, q.id)
}

// reservationCursorSep separates a cursor's fields: the expiry's Unix
// seconds, its nanoseconds within the second, and the id. Seconds and
// nanoseconds apart, because a single count of Unix nanoseconds overflows
// past year 2262, and a hold's TTL reaches further.
const reservationCursorSep = "_"

// reservationCursorFields is how many fields a cursor has.
const reservationCursorFields = 3

func (p reservationPlace) String() string {
	return strings.Join([]string{
		strconv.FormatInt(p.expires.Unix(), 10),
		strconv.Itoa(p.expires.Nanosecond()),
		p.id.String(),
	}, reservationCursorSep)
}

func parseReservationPlace(cursor string) (reservationPlace, error) {
	invalid := fmt.Errorf("%w: memstore: cursor %q", limes.ErrInvalidRequest, cursor)
	fields := strings.Split(cursor, reservationCursorSep)
	if len(fields) != reservationCursorFields {
		return reservationPlace{}, invalid
	}
	sec, err1 := strconv.ParseInt(fields[0], 10, 64)
	nsec, err2 := strconv.ParseInt(fields[1], 10, 64)
	id, err3 := uuid.Parse(fields[2])
	if err1 != nil || err2 != nil || err3 != nil || nsec < 0 || nsec >= int64(time.Second) {
		return reservationPlace{}, invalid
	}
	return reservationPlace{time.Unix(sec, nsec), id}, nil
}

// Release ends a reservation without charging. Idempotent.
func (s *UsageStore[TX]) Release(_ context.Context, req limes.ReleaseRequest) error {
	reservationID := req.ReservationID
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.holds[reservationID]; ok {
		s.applyHoldLocked(h, -1)
		delete(s.holds, reservationID)
	}
	return nil
}

// ReleaseExpired releases every hold past its expiry.
func (s *UsageStore[TX]) ReleaseExpired(_ context.Context, req limes.ReleaseExpiredRequest) (limes.ReleaseExpiredResult, error) {
	limit, err := housekeepingLimit(req.Limit)
	if err != nil {
		return limes.ReleaseExpiredResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowFn()
	var due []hold
	for _, h := range s.holds {
		if !h.expires.After(now) {
			due = append(due, h)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].expires.Before(due[j].expires) })
	if len(due) > limit {
		due = due[:limit]
	}
	for _, h := range due {
		s.applyHoldLocked(h, -1)
		delete(s.holds, h.id)
	}
	return limes.ReleaseExpiredResult{Released: int64(len(due))}, nil
}

// Refund returns spend from one ledger entry to the counters it debited.
func (s *UsageStore[TX]) Refund(_ context.Context, req limes.RefundRequest) (limes.RefundReceipt, error) {
	if err := req.Validate(); err != nil {
		return limes.RefundReceipt{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	i, ok := s.byCharge[req.ChargeID]
	if !ok {
		return limes.RefundReceipt{}, limes.ErrChargeNotFound
	}
	e := &s.ledger[i]
	receipt := limes.RefundReceipt{ChargeID: e.ID, TenantID: e.TenantID, UnitCode: e.UnitCode}
	remaining := e.Amount - e.RefundedAmount
	amount := req.Amount
	if req.Remaining {
		amount = remaining
	}
	if amount > remaining {
		return limes.RefundReceipt{}, fmt.Errorf("%w: %v requested, %v of %v left",
			limes.ErrRefundExceedsCharge, amount, remaining, e.Amount)
	}
	if amount == 0 {
		return receipt, nil
	}
	for _, id := range append([]uuid.UUID{e.CapabilityID}, e.Ancestors...) {
		if u, ok := s.usage[id]; ok {
			u.SpentAmount = max0(u.SpentAmount - amount)
			s.usage[id] = u
		}
	}
	// A charge of a period since closed is history: the current period's
	// spend never counted it, so it has nothing to give back.
	if b, ok := s.budgets[e.TenantID]; ok && e.tenantPeriod == s.periods[e.TenantID] {
		b.SpentAmount = max0(b.SpentAmount - amount)
		b.UpdatedAt = s.nowFn()
		s.budgets[e.TenantID] = b
	}
	for _, id := range e.Copies {
		if cc, ok := s.copies[string(id)]; ok {
			cc.spent = max0(cc.spent - amount)
			s.copies[string(id)] = cc
		}
	}
	e.RefundedAmount += amount
	receipt.RefundedAmount = amount
	return receipt, nil
}

func (s *UsageStore[TX]) GetUsage(_ context.Context, capID uuid.UUID) (limes.Usage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.usage[capID]
	if !ok {
		return limes.Usage{}, limes.ErrUsageNotFound
	}
	return u, nil
}

// CopyUsage implements limes.CopyUsageReader.
func (s *UsageStore[TX]) CopyUsage(_ context.Context, revocationIDs [][]byte) ([]limes.CopyUsage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []limes.CopyUsage
	for _, id := range revocationIDs {
		if c, ok := s.copies[string(id)]; ok {
			out = append(out, limes.CopyUsage{
				RevocationID: slices.Clone(id), CapabilityID: c.capID,
				RequestCount: c.requests, SpentAmount: c.spent, ReservedAmount: c.reserved,
			})
		}
	}
	return out, nil
}

func (s *UsageStore[TX]) GetTenantBudget(_ context.Context, tenantID uuid.UUID) (limes.TenantBudget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.budgets[tenantID]
	if !ok {
		return limes.TenantBudget{}, limes.ErrTenantBudgetNotFound
	}
	return b, nil
}

func (s *UsageStore[TX]) SetTenantBudget(_ context.Context, args limes.SetTenantBudgetRequest) (limes.TenantBudget, error) {
	if err := args.Validate(); err != nil {
		return limes.TenantBudget{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	b, exists := s.budgets[args.TenantID]

	// SetTenantBudgetRequest.ExpectedVersion's contract, held as every store
	// must hold it, or tests pass against a memstore that permits writes a
	// real store refuses. A missing budget expects version 0; an existing one
	// expects its current version.
	want := int64(0)
	if exists {
		want = b.ResourceVersion
	}
	if args.ExpectedVersion != want {
		return limes.TenantBudget{}, limes.ErrTenantBudgetVersionMismatch
	}

	unit := args.UnitCode
	if unit == "" {
		unit = b.UnitCode
	}
	normalised, err := limes.NormalizeUnitCode(unit)
	if err != nil {
		return limes.TenantBudget{}, err
	}
	// A reset closes the spend, never the holds still open in the old unit.
	if exists && b.UnitCode != "" && normalised != b.UnitCode &&
		(b.ReservedAmount > 0 || (b.SpentAmount > 0 && !args.ResetSpend)) {
		return limes.TenantBudget{}, fmt.Errorf("%w: tenant %s has %s counted in %s",
			limes.ErrUnitCodeMismatch, args.TenantID, b.SpentAmount+b.ReservedAmount, b.UnitCode)
	}

	b.TenantID = args.TenantID
	b.Ceiling = args.Ceiling
	b.UnitCode = normalised
	b.PeriodEnd = args.PeriodEnd
	if args.ResetSpend {
		b.SpentAmount = 0
		b.PeriodStart = s.nowFn()
		s.periods[args.TenantID]++
	}
	if b.PeriodStart.IsZero() {
		b.PeriodStart = s.nowFn()
	}
	b.UpdatedAt = s.nowFn()
	b.ResourceVersion++
	s.budgets[args.TenantID] = b
	return b, nil
}

func (s *UsageStore[TX]) ListTenantBudgets(_ context.Context, args limes.ListTenantBudgetsRequest) (limes.Page[limes.TenantBudget], error) {
	if err := args.Validate(); err != nil {
		return limes.Page[limes.TenantBudget]{}, err
	}
	var after *budgetRow
	if args.Cursor != "" {
		c, err := decodeTenantBudgetCursor(args.Cursor)
		if err != nil {
			return limes.Page[limes.TenantBudget]{}, err
		}
		pct, _ := strconv.ParseFloat(c.utilization, 64) // decodeTenantBudgetCursor checked it
		after = &budgetRow{class: c.class, pct: pct, tenant: c.tenantID}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	rows := make([]budgetRow, 0, len(s.budgets))
	for _, b := range s.budgets {
		if args.UnlimitedOnly && !b.Ceiling.Unlimited {
			continue
		}
		row := budgetRow{class: classOf(b.Ceiling), tenant: b.TenantID, budget: b}
		if row.class == classBounded {
			row.pct = float64(b.SpentAmount) / float64(b.Ceiling.Max) * percent
		}
		// A ceiling of 0 is spent through, at every threshold; an
		// unlimited one is at none.
		if args.ThresholdPct > 0 && (row.class == classUnlimited || row.class == classBounded && row.pct < args.ThresholdPct) {
			continue
		}
		if after != nil && !after.before(row) {
			continue
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].before(rows[j]) })

	limit := int(limes.PageLimit(args.Limit))
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		// 'f': a plain decimal, which the cursor reads back exactly.
		next = tenantBudgetCursor{
			class: last.class, utilization: strconv.FormatFloat(last.pct, 'f', -1, 64), tenantID: last.tenant,
		}.encode()
	}
	out := make([]limes.TenantBudget, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.budget)
	}
	return limes.Page[limes.TenantBudget]{Items: out, Next: next}, nil
}

// budgetRow is a tenant budget in ListTenantBudgets' order.
type budgetRow struct {
	class  ceilingClass
	pct    float64 // unclamped utilization; 0 outside classBounded
	tenant uuid.UUID
	budget limes.TenantBudget
}

// ceilingClass is where a tenant's kind of ceiling ranks in
// ListTenantBudgets: a ceiling of 0 allows nothing and leads, a bounded one
// ranks by its utilization, and an unlimited one has none and trails.
type ceilingClass int

const (
	classZero ceilingClass = iota
	classBounded
	classUnlimited
)

func classOf(c limes.BudgetCeiling) ceilingClass {
	switch {
	case c.Unlimited:
		return classUnlimited
	case c.Max == 0:
		return classZero
	}
	return classBounded
}

// before reports whether r comes before o: by class, then higher utilization
// first, then the lower tenant id.
func (r budgetRow) before(o budgetRow) bool {
	if r.class != o.class {
		return r.class < o.class
	}
	if r.pct != o.pct {
		return r.pct > o.pct
	}
	return uuidLess(r.tenant, o.tenant)
}

// releaseHoldsLocked releases every hold of a capability gone reports as
// gone. s.mu must be held.
func (s *UsageStore[TX]) releaseHoldsLocked(gone func(capID uuid.UUID) bool) {
	for id, h := range s.holds {
		if gone(h.capID) {
			s.applyHoldLocked(h, -1)
			delete(s.holds, id)
		}
	}
}

// DeleteUsage drops the capability's counters and releases its holds first:
// nothing could settle or release them once it is gone, and they would keep
// counting against its ancestors and its tenant.
func (s *UsageStore[TX]) DeleteUsage(_ context.Context, req limes.DeleteUsageRequest) error {
	capID := req.CapabilityID
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseHoldsLocked(func(id uuid.UUID) bool { return id == capID })
	delete(s.usage, capID)
	maps.DeleteFunc(s.copies, func(_ string, c copyCounter) bool { return c.capID == capID })
	s.forgetAncestorLocked(func(id uuid.UUID) bool { return id == capID })
	return nil
}

// forgetAncestorLocked drops every id gone reports from the ancestors of the
// holds that remain: a descendant's hold still names a dropped row's
// capability, and releasing or settling it later must not write that row
// back. s.mu must be held.
func (s *UsageStore[TX]) forgetAncestorLocked(gone func(uuid.UUID) bool) {
	for id, h := range s.holds {
		if slices.ContainsFunc(h.ancestors, gone) {
			h.ancestors = slices.DeleteFunc(slices.Clone(h.ancestors), gone)
			s.holds[id] = h
		}
	}
}

func (s *UsageStore[TX]) PurgeOrphans(_ context.Context, req limes.PurgeOrphansRequest) (limes.PurgeOrphansResult, error) {
	limit, err := housekeepingLimit(req.Limit)
	if err != nil {
		return limes.PurgeOrphansResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	gone := func(id uuid.UUID) bool { return !s.records.exists(id) }
	// Every capability with a row or a hold that is no longer on record, in
	// a fixed order so batches are repeatable.
	orphans := map[uuid.UUID]bool{}
	for id := range s.usage {
		if gone(id) {
			orphans[id] = true
		}
	}
	for _, h := range s.holds {
		if gone(h.capID) {
			orphans[h.capID] = true
		}
	}
	ids := slices.SortedFunc(maps.Keys(orphans), func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	if len(ids) > limit {
		ids = ids[:limit]
	}
	batch := map[uuid.UUID]bool{}
	for _, id := range ids {
		batch[id] = true
	}
	// Holds first: releasing one writes its ancestors' rows, which a later
	// batch purges if they are orphans too.
	s.releaseHoldsLocked(func(id uuid.UUID) bool { return batch[id] })
	for id := range batch {
		delete(s.usage, id)
	}
	s.forgetAncestorLocked(func(id uuid.UUID) bool { return batch[id] })
	maps.DeleteFunc(s.copies, func(_ string, c copyCounter) bool { return batch[c.capID] })
	return limes.PurgeOrphansResult{Purged: int64(len(ids))}, nil
}

// housekeepingLimit resolves a batch limit, refusing a negative one.
func housekeepingLimit(limit int) (int, error) {
	if limit < 0 {
		return 0, fmt.Errorf("%w: memstore: housekeeping limit %d is negative", limes.ErrInvalidRequest, limit)
	}
	if limit == 0 {
		return limes.DefaultHousekeepingLimit, nil
	}
	return limit, nil
}

func max0(n limes.Nanos) limes.Nanos {
	if n < 0 {
		return 0
	}
	return n
}

// Compile-time proof that both contracts are satisfied by a consumer holding
// nothing but this module. If either assertion breaks, a third party cannot
// implement the contract either.
var (
	_ limes.Store                = (*Store)(nil)
	_ limes.IssuanceStore        = (*Store)(nil)
	_ limes.UsageStore[struct{}] = (*UsageStore[struct{}])(nil)
)

var _ limes.CopyUsageReader = (*UsageStore[struct{}])(nil)
