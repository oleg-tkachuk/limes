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
// NewLedger reads a usage store's ledger as a meter.LedgerReader, NewCensus
// counts a record store's records as a limes.RecordCensus, and the
// stores erase a tenant as limes.TenantRecordEraser and
// meter.TenantUsageEraser.
//
// It is NOT a production store: each store keeps its maps behind one mutex,
// which the usage store holds while onCharged runs, nothing survives a
// restart, and the charges ledger only grows for as long as the store lives:
// EraseTenantUsage is all that removes an entry.
package memstore

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/link"
	"github.com/oleg-tkachuk/limes/meter"
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
	// revokedCopies holds each revoked Biscuit copy under the capability it
	// was revoked under, and revokedIDs counts those entries per revocation
	// id: a copy's id is a block signature, which a chain of another
	// capability can carry too, so one capability's purge must not take
	// another's revocation with it, while a lookup refuses the id under any.
	revokedCopies map[copyKey]limes.BiscuitRevocation
	revokedIDs    map[string]int
	nowFn         func() time.Time
}

// UsageStore implements meter.UsageStore — request and spend counters. It
// has no transactions, so onCharged runs on the caller's context.
type UsageStore struct {
	mu sync.Mutex

	usage map[uuid.UUID]meter.Usage
	// tenantOf is the tenant of each capability with a usage row, so a
	// row outliving its record still names its tenant (EraseTenantUsage).
	tenantOf map[uuid.UUID]uuid.UUID
	budgets  map[uuid.UUID]meter.TenantBudget
	ledger   []ledgerEntry
	// The ledger's indexes, each to a position in ledger: by charge id, by
	// (capability, external ref) and by the reservation a charge settled.
	byCharge      map[uuid.UUID]int
	byRef         map[refKey]int
	byReservation map[uuid.UUID]int
	records       *Store // lineage and ceilings of ancestors
	holds         map[uuid.UUID]hold
	nowFn         func() time.Time

	// copies counts each Biscuit copy that set limits of its own, by its
	// capability and its block's revocation id; at most maxCopies of one
	// capability.
	copies    map[copyKey]copyCounter
	maxCopies int64
	// copyCount is how many copies s.copies counts for each capability, so
	// that admitting one reads one entry rather than every copy.
	copyCount map[uuid.UUID]int64

	// periods counts each tenant's accounting periods: SetTenantBudget with
	// ResetSpend starts the next one. A ledger entry records the period it
	// was counted in, so a refund leaves a later period's spend alone.
	periods map[uuid.UUID]uint64

	// erased marks the tenants EraseTenantUsage removed, which nothing
	// counts again until PurgeOrphans finds none of their records left.
	erased map[uuid.UUID]struct{}
}

// refKey is what ChargeRequest.ExternalRef makes a charge idempotent on.
type refKey struct {
	capID uuid.UUID
	ref   string
}

// copyKey names one Biscuit copy under one capability. The revocation id
// alone does not: biscuit-go signs a block without the signature before it,
// so a block can be grafted onto a chain of another capability.
type copyKey struct {
	capID uuid.UUID
	id    string
}

func copyKeyOf(capID uuid.UUID, revocationID []byte) copyKey {
	return copyKey{capID: capID, id: string(revocationID)}
}

// hasKey reports whether m holds k.
func hasKey[K comparable, V any](m map[K]V, k K) bool {
	_, ok := m[k]
	return ok
}

// admitCopiesLocked refuses copies that would take the subject's capability
// past its quota of counted copies — its own, bounded by the store's — the
// ones it already counts aside. s.mu must be held.
//
// Every capability on the chain — the subject's, under the quota its token
// carries, and each ancestor, under the quota its record carries — counts
// the copies of its whole subtree against its quota, as it counts their
// requests and spend: a delegated child does not multiply the copies its
// root allows. A copy whose capability is no longer on record counts under
// no ancestor.
func (s *UsageStore) admitCopiesLocked(subject limes.MeterSubject, ancestors []limes.Capability) error {
	capID, copies := subject.CapabilityID(), subject.Copies()
	quota := subject.CopyQuota().Bounded(s.maxCopies)
	added := map[copyKey]bool{}
	for _, c := range copies {
		if key := copyKeyOf(capID, c.RevocationID); !hasKey(s.copies, key) {
			added[key] = true
		}
	}
	if len(added) == 0 {
		return nil
	}
	counted := s.copyCount[capID]
	if counted+int64(len(added)) > quota {
		return fmt.Errorf("%w: capability %s counts %d, quota %d", meter.ErrCopyLimitExceeded, capID, counted, quota)
	}
	chain := []struct {
		id    uuid.UUID
		quota limes.CopyQuota
	}{{capID, subject.CopyQuota()}}
	for _, a := range ancestors {
		chain = append(chain, struct {
			id    uuid.UUID
			quota limes.CopyQuota
		}{a.ID, a.Caveats.Copies})
	}
	for _, c := range chain {
		if c.quota.Unlimited {
			continue
		}
		if under := s.subtreeCopiesLocked(c.id); !c.quota.Admits(under + int64(len(added))) {
			return fmt.Errorf("%w: capability %s and those delegated from it count %d, quota %s", meter.ErrCopyLimitExceeded, c.id, under, c.quota)
		}
	}
	return nil
}

// subtreeCopiesLocked is how many copies the store counts for top and every
// capability delegated from it, as far as their records reach. s.mu must be
// held.
func (s *UsageStore) subtreeCopiesLocked(top uuid.UUID) int64 {
	var n int64
	for capID, count := range s.copyCount {
		if capID == top {
			n += count
			continue
		}
		ancestors, err := s.lineage(capID)
		if err != nil {
			continue // an orphan's place in a tree is no longer known
		}
		if slices.ContainsFunc(ancestors, func(a limes.Capability) bool { return a.ID == top }) {
			n += count
		}
	}
	return n
}

// putCopyLocked stores a copy's counters, counting the copy for its
// capability when it is new. s.mu must be held.
func (s *UsageStore) putCopyLocked(key copyKey, cc copyCounter) {
	if !hasKey(s.copies, key) {
		s.copyCount[key.capID]++
	}
	s.copies[key] = cc
}

// dropCopiesLocked removes every copy of each capability gone says, with
// its count. s.mu must be held.
func (s *UsageStore) dropCopiesLocked(gone func(uuid.UUID) bool) {
	maps.DeleteFunc(s.copies, func(k copyKey, _ copyCounter) bool { return gone(k.capID) })
	maps.DeleteFunc(s.copyCount, func(id uuid.UUID, _ int64) bool { return gone(id) })
}

// copyCounter is one Biscuit copy's own counters.
type copyCounter struct {
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
	// Overrun reports that the charge crossed a ceiling (OverrunPolicyRecord).
	Overrun bool

	// tenantPeriod is the tenant's accounting period the charge was counted
	// in (UsageStore.periods).
	tenantPeriod uint64
}

// StoreConfig configures a Store.
type StoreConfig struct {
	// Now is the clock, read as a wall clock (wallClock). Nil: time.Now.
	Now func() time.Time
}

// wallClock is now with its monotonic reading dropped. time.Time compares two
// times by their monotonic readings when both carry one, and by the wall
// clock otherwise; a time parsed from a cursor carries none. A store keeping
// one would order its rows by one clock and seek its cursors by the other,
// and after a step of the wall clock the two disagree: a page skips or
// repeats a row. A store over a database keeps wall times alone; so does
// this one.
func wallClock(now func() time.Time) func() time.Time {
	return func() time.Time { return now().Round(0) }
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
		nowFn:    wallClock(cfg.Now),

		revokedCopies: map[copyKey]limes.BiscuitRevocation{},
		revokedIDs:    map[string]int{},
	}, nil
}

// UsageStoreConfig configures a UsageStore.
type UsageStoreConfig struct {
	// Records is how the usage store finds each capability's ancestors and
	// their ceilings, and which capabilities still exist for PurgeOrphans.
	// Required: without it no ancestor's ceiling would bind, which a store
	// over a database never does.
	Records *Store
	// Now is the clock, read as a wall clock (wallClock). Nil: time.Now.
	Now func() time.Time
	// MaxCopiesPerCapability is how many Biscuit copies the store counts
	// for one capability (meter.Meter, "Copies of a Biscuit"). Zero:
	// meter.DefaultMaxCopiesPerCapability; negative is refused.
	MaxCopiesPerCapability int
}

// NewUsageStore returns an empty usage store.
//
//	memstore.NewUsageStore(memstore.UsageStoreConfig{Records: records})
func NewUsageStore(cfg UsageStoreConfig) (*UsageStore, error) {
	if cfg.Records == nil {
		return nil, errors.New("memstore: UsageStoreConfig.Records required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	switch {
	case cfg.MaxCopiesPerCapability < 0:
		return nil, fmt.Errorf("memstore: UsageStoreConfig.MaxCopiesPerCapability %d is negative", cfg.MaxCopiesPerCapability)
	case cfg.MaxCopiesPerCapability == 0:
		cfg.MaxCopiesPerCapability = meter.DefaultMaxCopiesPerCapability
	}
	return &UsageStore{
		maxCopies: int64(cfg.MaxCopiesPerCapability),
		usage:     map[uuid.UUID]meter.Usage{},
		tenantOf:  map[uuid.UUID]uuid.UUID{},
		budgets:   map[uuid.UUID]meter.TenantBudget{},
		holds:     map[uuid.UUID]hold{},
		copies:    map[copyKey]copyCounter{},
		copyCount: map[uuid.UUID]int64{},
		periods:   map[uuid.UUID]uint64{},
		erased:    map[uuid.UUID]struct{}{},

		byCharge:      map[uuid.UUID]int{},
		byRef:         map[refKey]int{},
		byReservation: map[uuid.UUID]int{},
		records:       cfg.Records,
		nowFn:         wallClock(cfg.Now),
	}, nil
}

// ─── limes.Store ──────────────────────────────────────────────────────

// Insert records req.Capability with req.IssuedBy beside it, so the in-memory
// store answers the same attribution questions a relational one does — a test
// double that silently dropped the initiator would let an unattributed
// issuance pass.
func (s *Store) Insert(_ context.Context, req limes.InsertRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	c, issuedBy := req.Capability, req.IssuedBy
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.caps[c.ID]; ok {
		return fmt.Errorf("%w: %s", limes.ErrAlreadyExists, c.ID)
	}
	if c.ParentID != uuid.Nil {
		parent, ok := s.caps[c.ParentID]
		if !ok {
			return fmt.Errorf("%w: memstore: parent %s is not on record", limes.ErrInvalidRequest, c.ParentID)
		}
		if c.ExpiresAt.After(parent.ExpiresAt) {
			return fmt.Errorf("%w: expires %s, after its parent %s", limes.ErrDelegationTooWide, c.ExpiresAt, parent.ExpiresAt)
		}
		if c.Subject.TenantID != parent.Subject.TenantID {
			return fmt.Errorf("%w: tenant %s, not its parent's %s", limes.ErrDelegationTooWide, c.Subject.TenantID, parent.Subject.TenantID)
		}
		if c.Caveats.Budget.UnitCode != parent.Caveats.Budget.UnitCode {
			return fmt.Errorf("%w: budget in %q, under a parent in %q", limes.ErrUnitCodeMismatch, c.Caveats.Budget.UnitCode, parent.Caveats.Budget.UnitCode)
		}
		// The child's ancestors are its parent and the parent's own.
		ancestors, err := s.ancestorsLocked(c.ParentID)
		if err != nil {
			return err
		}
		if len(ancestors) >= limes.MaxDelegationDepth {
			return fmt.Errorf("%w: %s would have more than %d ancestors", limes.ErrDelegationTooDeep, c.ID, limes.MaxDelegationDepth)
		}
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
	return s.revokedLocked(id)
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

// expiryOf is when id's record expires: the zero time for one that never
// does or is not on record.
func (s *Store) expiryOf(id uuid.UUID) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.caps[id].ExpiresAt
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

// holdsTenant reports whether any record of tenant is on record.
func (s *Store) holdsTenant(tenant uuid.UUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.caps {
		if c.Subject.TenantID == tenant {
			return true
		}
	}
	return false
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
	if err := args.Validate(); err != nil {
		return err
	}
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
		if s.revokedIDs[string(id)] > 0 {
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
	key := copyKeyOf(capID, revocationID)
	if _, ok := s.revokedCopies[key]; !ok { // the first entry stands
		s.revokedCopies[key] = limes.BiscuitRevocation{
			CapabilityID: capID, RevocationID: revocationID,
			RevokedAt: s.nowFn(), Note: args.Note, Actor: args.Actor,
		}
		s.revokedIDs[key.id]++
	}
	return nil
}

// GetBiscuitRevocation returns what RevokeBiscuit wrote for the copy.
func (s *Store) GetBiscuitRevocation(_ context.Context, bc limes.BiscuitCopy) (limes.BiscuitRevocation, error) {
	if err := bc.Validate(); err != nil {
		return limes.BiscuitRevocation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.revokedCopies[copyKeyOf(bc.CapabilityID(), bc.RevocationID())]
	if !ok {
		return limes.BiscuitRevocation{}, limes.ErrNotFound
	}
	r.RevocationID = slices.Clone(r.RevocationID)
	return r, nil
}

// Purge implements limes.Store, oldest expiry first and, among records
// expiring together, the deepest first, so a batch cut at its limit takes a
// child before its parent: Insert refuses a child that outlives its parent,
// so a child is due no later than its parent.
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
		if c.ExpiresAt.Before(cutoff) {
			due = append(due, c)
		}
	}
	depth := make(map[uuid.UUID]int, len(due))
	for _, c := range due {
		depth[c.ID] = s.depthLocked(c)
	}
	slices.SortFunc(due, func(a, b limes.Capability) int {
		return cmp.Or(a.ExpiresAt.Compare(b.ExpiresAt), cmp.Compare(depth[b.ID], depth[a.ID]), bytes.Compare(a.ID[:], b.ID[:]))
	})
	if len(due) > limit {
		due = due[:limit]
	}
	for _, c := range due {
		delete(s.caps, c.ID)
		delete(s.revoked, c.ID)
		delete(s.issuedBy, c.ID)
		s.forgetCopyRevocationsLocked(c.ID)
	}
	return limes.PurgeResult{Purged: int64(len(due))}, nil
}

// EraseTenantRecords implements limes.TenantRecordEraser: it revokes each
// of the tenant's capabilities not revoked yet and removes those Purge would
// remove at limes.MinPurgeRetention, leaving the rest to Purge.
func (s *Store) EraseTenantRecords(_ context.Context, req limes.EraseTenantRequest) (limes.EraseTenantResult, error) {
	if err := req.Validate(); err != nil {
		return limes.EraseTenantResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowFn()
	cutoff := now.Add(-limes.MinPurgeRetention)
	var out limes.EraseTenantResult
	for id, c := range s.caps {
		if c.Subject.TenantID != req.TenantID {
			continue
		}
		if _, ok := s.revoked[id]; !ok {
			s.revokeLocked(id, limes.Revocation{RevokedAt: now, Note: req.Note, Actor: req.Actor})
			out.Revoked++
		}
		// A child expires no later than its parent, so the records due
		// hold each one's descendants: none is left without its parent.
		if !c.ExpiresAt.Before(cutoff) {
			out.Kept++
			continue
		}
		delete(s.caps, id)
		delete(s.revoked, id)
		delete(s.issuedBy, id)
		s.forgetCopyRevocationsLocked(id)
		out.Removed++
	}
	return out, nil
}

// forgetCopyRevocationsLocked drops the copy revocations listed under capID,
// and with them only that capability's share of each id. s.mu must be held.
func (s *Store) forgetCopyRevocationsLocked(capID uuid.UUID) {
	maps.DeleteFunc(s.revokedCopies, func(k copyKey, _ limes.BiscuitRevocation) bool {
		if k.capID != capID {
			return false
		}
		if s.revokedIDs[k.id]--; s.revokedIDs[k.id] == 0 {
			delete(s.revokedIDs, k.id)
		}
		return true
	})
}

// depthLocked is how many of c's ancestors are on record, counted no
// further than one past MaxDelegationDepth: enough to order a purge, and
// bounded on a chain too deep or a corrupt cycle. s.mu must be held.
func (s *Store) depthLocked(c limes.Capability) int {
	n := 0
	for ok := true; ok && c.ParentID != uuid.Nil && n <= limes.MaxDelegationDepth; n++ {
		c, ok = s.caps[c.ParentID]
		if !ok {
			break
		}
	}
	return n
}

// ListByPrincipal pages one principal's capabilities, as the Store contract
// orders them.
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

// ─── meter.UsageStore ─────────────────────────────────────────────

// sameCopies reports whether two lists of copy limits name the same copies,
// in the same order: a hold is settled under the copies it was made under.
func sameCopies(a, b []limes.CopyCeiling) bool {
	return slices.EqualFunc(a, b, func(x, y limes.CopyCeiling) bool { return bytes.Equal(x.RevocationID, y.RevocationID) })
}

// sameCopyIDs reports whether a ledger entry's copy ids are those of the
// copy limits a subject carries, in order.
func sameCopyIDs(ids [][]byte, copies []limes.CopyCeiling) bool {
	return slices.EqualFunc(ids, copies, func(id []byte, c limes.CopyCeiling) bool { return bytes.Equal(id, c.RevocationID) })
}

// lineage returns the ancestors whose counters move with capID's, and
// ErrNotFound when capID itself is not on record: with no record its
// ancestors cannot be read, and metering it alone would let it spend past
// every live ancestor's ceiling.
func (s *UsageStore) lineage(capID uuid.UUID) ([]limes.Capability, error) {
	return s.records.lineageOf(capID)
}

// Bump returns ErrRequestLimitExceeded WITHOUT mutating when the
// increment would cross the capability's ceiling or any ancestor's. A
// rejected call must be safe to retry.
func (s *UsageStore) Bump(_ context.Context, req meter.BumpRequest) (meter.BumpReceipt, error) {
	if err := req.Validate(); err != nil {
		return meter.BumpReceipt{}, err
	}
	unit := req.Subject.UnitCode()
	// The lineage is read under the usage lock, as DeleteUsage and PurgeOrphans
	// take it: read before, an ancestor they dropped meanwhile would have
	// its row written back.
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.notErasedLocked(req.Subject.TenantID()); err != nil {
		return meter.BumpReceipt{}, err
	}
	ancestors, err := s.lineage(req.Subject.CapabilityID())
	if err != nil {
		return meter.BumpReceipt{}, err
	}
	if err := s.admitCopiesLocked(req.Subject, ancestors); err != nil {
		return meter.BumpReceipt{}, err
	}
	u := s.usage[req.Subject.CapabilityID()]
	next := u.RequestCount + 1
	for _, c := range req.Subject.Copies() {
		if !c.AdmitsRequests(s.copies[copyKeyOf(req.Subject.CapabilityID(), c.RevocationID)].requests + 1) {
			return meter.BumpReceipt{RequestCount: u.RequestCount}, fmt.Errorf("%w: copy %x", limes.ErrRequestLimitExceeded, c.RevocationID)
		}
	}
	if !req.Subject.Requests().Admits(next) {
		return meter.BumpReceipt{RequestCount: u.RequestCount}, limes.ErrRequestLimitExceeded
	}
	for _, a := range ancestors {
		if !a.Caveats.Requests.Admits(s.usage[a.ID].RequestCount + 1) {
			return meter.BumpReceipt{RequestCount: u.RequestCount}, fmt.Errorf("%w: ancestor %s", limes.ErrRequestLimitExceeded, a.ID)
		}
	}
	for _, a := range ancestors {
		au := s.usage[a.ID]
		au.CapabilityID = a.ID
		au.RequestCount++
		if au.UnitCode == "" {
			au.UnitCode = unit
		}
		s.putUsageLocked(a.ID, req.Subject.TenantID(), au)
	}
	for _, c := range req.Subject.Copies() {
		key := copyKeyOf(req.Subject.CapabilityID(), c.RevocationID)
		cc := s.copies[key]
		cc.requests++
		s.putCopyLocked(key, cc)
	}
	u.CapabilityID = req.Subject.CapabilityID()
	u.RequestCount = next
	if u.UnitCode == "" {
		u.UnitCode = unit
	}
	s.putUsageLocked(req.Subject.CapabilityID(), req.Subject.TenantID(), u)
	return meter.BumpReceipt{RequestCount: next}, nil
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
func (s *UsageStore) Charge(
	ctx context.Context,
	req meter.ChargeRequest,
	onCharged func(ctx context.Context, c meter.Charged) error,
) (meter.ChargeReceipt, error) {
	if err := req.Validate(); err != nil {
		return meter.ChargeReceipt{}, err
	}
	unit := req.Subject.UnitCode()
	// The lineage is read under the usage lock, as DeleteUsage and PurgeOrphans
	// take it: read before, an ancestor they dropped meanwhile would have
	// its row written back.
	s.mu.Lock()
	defer s.mu.Unlock()
	// A replay is answered before the lineage is read: it moves nothing, so
	// it needs no record, and a capability purged since still gets its
	// receipt, as a settled hold does.
	if req.ExternalRef != "" {
		if e, ok := entryLocked(s, s.byRef, refKey{req.Subject.CapabilityID(), req.ExternalRef}); ok {
			// Another copy charges its own counters, and the receipt is the
			// first copy's, so it conflicts as another cost does. The error
			// names the ref, never the other charge.
			if e.Amount != req.Amount || e.UnitCode != unit || e.TenantID != req.Subject.TenantID() || !sameCopyIDs(e.Copies, req.Subject.Copies()) {
				return meter.ChargeReceipt{}, fmt.Errorf("%w: %q", meter.ErrExternalRefConflict, req.ExternalRef)
			}
			return s.replayLocked(e), nil
		}
	}
	if err := s.notErasedLocked(req.Subject.TenantID()); err != nil {
		return meter.ChargeReceipt{}, err
	}
	ancestors, err := s.lineage(req.Subject.CapabilityID())
	if err != nil {
		return meter.ChargeReceipt{}, err
	}
	if err := s.admitCopiesLocked(req.Subject, ancestors); err != nil {
		return meter.ChargeReceipt{}, err
	}
	return s.chargeLocked(ctx, req, unit, ancestors, nil, onCharged)
}

// ChargeByRef returns the charge a capability took under externalRef.
func (s *UsageStore) ChargeByRef(_ context.Context, req meter.ChargeByRefRequest) (meter.ChargeRecord, error) {
	if err := req.Validate(); err != nil {
		return meter.ChargeRecord{}, err
	}
	capID, externalRef := req.CapabilityID, req.ExternalRef
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := entryLocked(s, s.byRef, refKey{capID, externalRef})
	if !ok {
		return meter.ChargeRecord{}, meter.ErrChargeNotFound
	}
	return e.record(), nil
}

// GetCharge returns one charge of the ledger by its id.
func (s *UsageStore) GetCharge(_ context.Context, chargeID uuid.UUID) (meter.ChargeRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := entryLocked(s, s.byCharge, chargeID)
	if !ok {
		return meter.ChargeRecord{}, meter.ErrChargeNotFound
	}
	return e.record(), nil
}

// record is the entry as the Meter contract reads a charge back.
func (e ledgerEntry) record() meter.ChargeRecord {
	return meter.ChargeRecord{
		ID: e.ID, CapabilityID: e.CapabilityID, TenantID: e.TenantID, Amount: e.Amount, RefundedAmount: e.RefundedAmount,
		UnitCode: e.UnitCode, ExternalRef: e.ExternalRef, Overrun: e.Overrun, Op: e.Op, Actor: e.Actor, At: e.At,
	}
}

// entryLocked returns the ledger entry index files under key. s.mu must be
// held.
func entryLocked[K comparable](s *UsageStore, index map[K]int, key K) (ledgerEntry, bool) {
	i, ok := index[key]
	if !ok {
		return ledgerEntry{}, false
	}
	return s.ledger[i], true
}

// putUsageLocked writes capID's usage row, of tenant. s.mu must be held.
func (s *UsageStore) putUsageLocked(capID, tenant uuid.UUID, u meter.Usage) {
	s.usage[capID] = u
	s.tenantOf[capID] = tenant
}

// dropUsageLocked removes capID's usage row. s.mu must be held.
func (s *UsageStore) dropUsageLocked(capID uuid.UUID) {
	delete(s.usage, capID)
	delete(s.tenantOf, capID)
}

// appendLocked adds entry to the ledger and its indexes. s.mu must be held.
func (s *UsageStore) appendLocked(entry ledgerEntry) {
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
func (s *UsageStore) replayLocked(e ledgerEntry) meter.ChargeReceipt {
	return meter.ChargeReceipt{
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
// it together with the release of settling (when non-nil). The charge is
// spent on ancestors, the lineage as it stands; the hold is released from the
// ancestors that hold it, which a row dropped since may have left. Held
// amounts count against each ceiling, except the one being settled where it
// is held. s.mu must be held.
func (s *UsageStore) chargeLocked(
	ctx context.Context,
	req meter.ChargeRequest,
	unit string,
	ancestors []limes.Capability,
	settling *hold,
	onCharged func(ctx context.Context, c meter.Charged) error,
) (meter.ChargeReceipt, error) {
	var released limes.Nanos
	var holders []uuid.UUID
	reservationID := uuid.Nil
	if settling != nil {
		released = settling.amount
		holders = settling.ancestors
		reservationID = settling.id
	}
	// crossed decides a ceiling the charge would cross: under OverrunPolicyRecord
	// the charge goes on and the receipt says so; otherwise it is refused
	// with err, and nothing has been published.
	overrun := false
	crossed := func(err error) error {
		if req.Overrun == meter.OverrunPolicyRecord {
			overrun = true
			return nil
		}
		return err
	}

	u := s.usage[req.Subject.CapabilityID()]
	if err := s.checkCeilingsLocked(ceilingCheck{
		amount: req.Amount, released: released, holders: holders, maxBudget: req.Subject.Budget(), unit: unit,
		capID: req.Subject.CapabilityID(), tenantID: req.Subject.TenantID(), ancestors: ancestors, copies: req.Subject.Copies(),
	}, crossed); err != nil {
		return meter.ChargeReceipt{SpentAmount: u.SpentAmount, TenantID: req.Subject.TenantID(), UnitCode: unit}, err
	}
	stagedSpent := u.SpentAmount + req.Amount
	receipt := meter.ChargeReceipt{
		ChargeID: uuid.New(), SpentAmount: stagedSpent, Overrun: overrun, TenantID: req.Subject.TenantID(), UnitCode: unit,
	}

	// the caller's side effect, on the caller's context — this store has no
	// transaction to put in it — before anything is published
	if onCharged != nil {
		if err := onCharged(ctx, meter.Charged{Receipt: receipt}); err != nil {
			// Nothing has been published — nothing to compensate.
			return meter.ChargeReceipt{SpentAmount: u.SpentAmount, TenantID: req.Subject.TenantID(), UnitCode: unit}, err
		}
	}

	// ── commit: publish every staged mutation together ──
	u.CapabilityID = req.Subject.CapabilityID()
	u.SpentAmount = stagedSpent
	u.ReservedAmount = max0(u.ReservedAmount - released)
	u.UnitCode = unit
	s.putUsageLocked(req.Subject.CapabilityID(), req.Subject.TenantID(), u)
	for _, a := range ancestors {
		au := s.usage[a.ID]
		au.CapabilityID = a.ID
		au.SpentAmount += req.Amount
		if au.UnitCode == "" {
			au.UnitCode = unit
		}
		s.putUsageLocked(a.ID, req.Subject.TenantID(), au)
	}
	for _, id := range holders {
		if au, ok := s.usage[id]; ok {
			au.ReservedAmount = max0(au.ReservedAmount - released)
			s.putUsageLocked(id, req.Subject.TenantID(), au)
		}
	}
	b := s.tenantLocked(req.Subject.TenantID(), unit)
	b.SpentAmount += req.Amount
	b.ReservedAmount = max0(b.ReservedAmount - released)
	b.UpdatedAt = s.nowFn()
	s.budgets[req.Subject.TenantID()] = b
	copies := req.Subject.Copies()
	copyIDs := make([][]byte, 0, len(copies))
	for _, c := range copies {
		key := copyKeyOf(req.Subject.CapabilityID(), c.RevocationID)
		cc := s.copies[key]
		cc.spent += req.Amount
		cc.reserved = max0(cc.reserved - released)
		s.putCopyLocked(key, cc)
		copyIDs = append(copyIDs, c.RevocationID)
	}
	entry := ledgerEntry{
		ID: receipt.ChargeID, CapabilityID: req.Subject.CapabilityID(), Ancestors: ancestorIDs(ancestors),
		TenantID: req.Subject.TenantID(), Amount: req.Amount, UnitCode: unit,
		Op: req.Op, Actor: req.Actor, At: s.nowFn(), Copies: copyIDs,
		ExternalRef: req.ExternalRef, ReservationID: reservationID, Overrun: overrun,
		tenantPeriod: s.periods[req.Subject.TenantID()],
	}
	s.appendLocked(entry)
	return receipt, nil
}

// tenantLocked returns the tenant's aggregate row, or a new one without a
// ceiling, in unit when the row counts in none yet: the unit of what it
// counts, not one a ceiling set. The aggregate counts from
// the first charge or hold, not from the first SetTenantBudget, so a ceiling
// set later is measured against what the tenant has already spent and
// holds. s.mu must be held.
func (s *UsageStore) tenantLocked(tenantID uuid.UUID, unit string) meter.TenantBudget {
	if b, ok := s.budgets[tenantID]; ok {
		if b.Ceiling.UnitCode == "" {
			b.Ceiling.UnitCode = unit
		}
		return b
	}
	// A ceiling never set lifts nothing and limits nothing: the row only
	// counts, until SetTenantBudget gives it a ceiling.
	return meter.TenantBudget{
		TenantID: tenantID, Ceiling: limes.BudgetCeiling{Unlimited: true, UnitCode: unit}, PeriodStart: s.nowFn(),
	}
}

// ceilingCheck is an amount measured against every ceiling. released is
// the amount of a hold being settled by it, which no longer counts.
type ceilingCheck struct {
	amount, released limes.Nanos
	// holders are the ancestors that hold released; the capability, its
	// copies and its tenant always do.
	holders         []uuid.UUID
	maxBudget       limes.BudgetCeiling // the capability's own, from its token
	unit            string              // normalised
	capID, tenantID uuid.UUID
	ancestors       []limes.Capability
	copies          []limes.CopyCeiling
}

// checkCeilingsLocked measures c against the copy's ceilings innermost
// first, then the capability's, each ancestor's and the tenant aggregate's,
// with whatever is already spent and held, when c adds to it. Each ceiling c would cross goes
// to crossed, which returns the error to stop on or nil to carry on.
// s.mu must be held.
func (s *UsageStore) checkCeilingsLocked(c ceilingCheck, crossed func(error) error) error {
	if err := s.checkUnitsLocked(c); err != nil {
		return err
	}
	if err := s.checkRangeLocked(c); err != nil {
		return err
	}
	// A settle at or below its hold adds nothing to a counter that holds it,
	// so no ceiling there can refuse it — not even one crossed since the
	// hold was made. Only the excess must fit. An ancestor that does not
	// hold it takes the whole amount.
	withinHold := c.amount <= c.released
	for _, cp := range c.copies {
		cc := s.copies[copyKeyOf(c.capID, cp.RevocationID)]
		if withinHold {
			continue
		}
		if !cp.AdmitsBudget(cc.spent + cc.reserved - c.released + c.amount) {
			if err := crossed(fmt.Errorf("%w: copy %x", limes.ErrBudgetExceeded, cp.RevocationID)); err != nil {
				return err
			}
		}
	}
	u := s.usage[c.capID]
	if !withinHold && !c.maxBudget.Admits(u.SpentAmount+u.ReservedAmount-c.released+c.amount) {
		if err := crossed(limes.ErrBudgetExceeded); err != nil {
			return err
		}
	}
	for _, a := range c.ancestors {
		au := s.usage[a.ID]
		held := limes.Nanos(0)
		if slices.Contains(c.holders, a.ID) {
			held = c.released
		}
		if c.amount <= held {
			continue
		}
		if !a.Caveats.Budget.Admits(au.SpentAmount + au.ReservedAmount - held + c.amount) {
			if err := crossed(fmt.Errorf("%w: ancestor %s", limes.ErrBudgetExceeded, a.ID)); err != nil {
				return err
			}
		}
	}
	if b, ok := s.budgets[c.tenantID]; ok && !withinHold &&
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
func (s *UsageStore) checkUnitsLocked(c ceilingCheck) error {
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
	if b, ok := s.budgets[c.tenantID]; ok && differs(b.Ceiling.UnitCode) {
		return fmt.Errorf("%w: tenant %s counts in %s, not %s", limes.ErrUnitCodeMismatch, c.tenantID, b.Ceiling.UnitCode, c.unit)
	}
	return nil
}

// checkRangeLocked keeps every counter c would move within MaxNanos, the
// range a store holds. Unlike a ceiling it is not an overrun to record: past
// it the amount could not be counted at all. s.mu must be held.
func (s *UsageStore) checkRangeLocked(c ceilingCheck) error {
	fits := func(spent, reserved limes.Nanos) bool {
		return spent+max0(reserved-c.released) <= limes.MaxNanos-c.amount
	}
	ok := true
	for _, cp := range c.copies {
		cc := s.copies[copyKeyOf(c.capID, cp.RevocationID)]
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
		return fmt.Errorf("%w: %s more", meter.ErrCounterOverflow, c.amount)
	}
	return nil
}

// Reserve holds an amount against every ceiling Charge checks.
func (s *UsageStore) Reserve(_ context.Context, req meter.ReserveRequest) (meter.Reservation, error) {
	if err := req.Validate(); err != nil {
		return meter.Reservation{}, err
	}
	unit := req.Subject.UnitCode()
	ttl := req.TTL
	if ttl == 0 {
		ttl = meter.DefaultReservationTTL
	}
	// The lineage is read under the usage lock, as DeleteUsage and PurgeOrphans
	// take it: read before, an ancestor they dropped meanwhile would have
	// its row written back.
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.notErasedLocked(req.Subject.TenantID()); err != nil {
		return meter.Reservation{}, err
	}
	ancestors, err := s.lineage(req.Subject.CapabilityID())
	if err != nil {
		return meter.Reservation{}, err
	}
	if err := s.admitCopiesLocked(req.Subject, ancestors); err != nil {
		return meter.Reservation{}, err
	}

	refuse := func(err error) error { return err }
	if err := s.checkCeilingsLocked(ceilingCheck{
		amount: req.Amount, maxBudget: req.Subject.Budget(), unit: unit,
		capID: req.Subject.CapabilityID(), tenantID: req.Subject.TenantID(), ancestors: ancestors, copies: req.Subject.Copies(),
	}, refuse); err != nil {
		return meter.Reservation{}, err
	}

	// A hold ends with its capability at the latest: past it no one can
	// settle the hold, and it would keep the ancestors' budget held.
	now := s.nowFn()
	expires := now.Add(ttl)
	capExpires := s.records.expiryOf(req.Subject.CapabilityID())
	// A capability already expired could hold nothing anyone may settle.
	if !capExpires.IsZero() && !capExpires.After(now) {
		return meter.Reservation{}, limes.ErrExpired
	}
	if !capExpires.IsZero() && capExpires.Before(expires) {
		expires = capExpires
	}
	h := hold{
		id:    uuid.New(),
		capID: req.Subject.CapabilityID(), ancestors: ancestorIDs(ancestors), tenantID: req.Subject.TenantID(),
		amount: req.Amount, unit: unit, op: req.Op, actor: req.Actor,
		expires: expires, copies: req.Subject.Copies(),
	}
	s.applyHoldLocked(h, +1)
	s.holds[h.id] = h
	return h.reservation(), nil
}

// applyHoldLocked adds (sign +1) or removes (sign -1) a hold on every counter.
func (s *UsageStore) applyHoldLocked(h hold, sign limes.Nanos) {
	for _, id := range append([]uuid.UUID{h.capID}, h.ancestors...) {
		u := s.usage[id]
		u.CapabilityID = id
		u.ReservedAmount = max0(u.ReservedAmount + sign*h.amount)
		if u.UnitCode == "" {
			u.UnitCode = h.unit
		}
		s.putUsageLocked(id, h.tenantID, u)
	}
	b := s.tenantLocked(h.tenantID, h.unit)
	b.ReservedAmount = max0(b.ReservedAmount + sign*h.amount)
	b.UpdatedAt = s.nowFn()
	s.budgets[h.tenantID] = b
	for _, c := range h.copies {
		key := copyKeyOf(h.capID, c.RevocationID)
		cc := s.copies[key]
		cc.reserved = max0(cc.reserved + sign*h.amount)
		s.putCopyLocked(key, cc)
	}
}

// Settle charges the actual cost in place of a reservation.
func (s *UsageStore) Settle(
	ctx context.Context,
	req meter.SettleRequest,
	onCharged func(ctx context.Context, c meter.Charged) error,
) (meter.ChargeReceipt, error) {
	if err := req.Validate(); err != nil {
		return meter.ChargeReceipt{}, err
	}
	// A direct charge is filed under the zero reservation id, so the zero id
	// must not reach the replay lookup below.
	if req.ReservationID == uuid.Nil {
		return meter.ChargeReceipt{}, meter.ErrReservationNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	subject := req.Subject.CapabilityID()
	h, ok := s.holds[req.ReservationID]
	if !ok {
		// A reservation of another capability or tenant, or settled under
		// other copies, is none of this one's: its receipt is not this
		// subject's to read back either.
		if e, settled := entryLocked(s, s.byReservation, req.ReservationID); settled && e.CapabilityID == subject && e.TenantID == req.Subject.TenantID() && sameCopyIDs(e.Copies, req.Subject.Copies()) {
			if e.Amount != req.Amount {
				return meter.ChargeReceipt{}, fmt.Errorf("%w: %s", meter.ErrSettleConflict, req.ReservationID)
			}
			return s.replayLocked(e), nil
		}
	}
	if !ok || h.capID != subject || h.tenantID != req.Subject.TenantID() || !h.expires.After(s.nowFn()) || !sameCopies(h.copies, req.Subject.Copies()) {
		return meter.ChargeReceipt{}, meter.ErrReservationNotFound
	}
	ancestors, err := s.lineage(h.capID)
	if err != nil {
		return meter.ChargeReceipt{}, err
	}
	// The charge is the reservation's: its tenant, unit and the copy limits
	// it was made under, against the subject's own ceiling.
	budget := req.Subject.Budget()
	budget.UnitCode = h.unit
	held := link.NewMeterSubject(link.MeterSubjectFields{
		CapabilityID: h.capID, TenantID: h.tenantID, Requests: req.Subject.Requests(),
		Budget: budget, Copies: h.copies,
	}).(limes.MeterSubject)
	receipt, err := s.chargeLocked(ctx, meter.ChargeRequest{
		Subject: held, Amount: req.Amount, Op: h.op, Actor: h.actor, Overrun: req.Overrun,
	}, h.unit, ancestors, &h, onCharged)
	if err != nil {
		return receipt, err
	}
	delete(s.holds, req.ReservationID)
	return receipt, nil
}

// reservation is the hold as the Meter contract reads it back.
func (h hold) reservation() meter.Reservation {
	return meter.Reservation{
		ID: h.id, CapabilityID: h.capID, TenantID: h.tenantID, Amount: h.amount, UnitCode: h.unit,
		Op: h.op, Actor: h.actor, Copies: heldCopies(h.copies), ExpiresAt: h.expires,
	}
}

// heldCopies is the copies a hold is on, with their budgets alone.
func heldCopies(copies []limes.CopyCeiling) []meter.HeldCopy {
	if len(copies) == 0 {
		return nil
	}
	out := make([]meter.HeldCopy, len(copies))
	for i, c := range copies {
		h := meter.HeldCopy{RevocationID: slices.Clone(c.RevocationID)}
		if c.Budget != nil {
			b := *c.Budget
			h.Budget = &b
		}
		out[i] = h
	}
	return out
}

// GetReservation returns a hold that still counts, expired or not.
func (s *UsageStore) GetReservation(_ context.Context, reservationID uuid.UUID) (meter.Reservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.holds[reservationID]
	if !ok {
		return meter.Reservation{}, meter.ErrReservationNotFound
	}
	return h.reservation(), nil
}

// ListReservations returns the capability's own holds, soonest to expire first.
func (s *UsageStore) ListReservations(_ context.Context, req meter.ListReservationsRequest) (limes.Page[meter.Reservation], error) {
	if err := req.Validate(); err != nil {
		return limes.Page[meter.Reservation]{}, err
	}
	var after *reservationPlace
	if req.Cursor != "" {
		p, err := parseReservationPlace(req.Cursor)
		if err != nil {
			return limes.Page[meter.Reservation]{}, err
		}
		after = &p
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []meter.Reservation{}
	for _, h := range s.holds {
		if h.capID == req.CapabilityID && (after == nil || after.before(placeOf(h.reservation()))) {
			out = append(out, h.reservation())
		}
	}
	// Soonest to expire first, ties by id.
	sort.Slice(out, func(i, j int) bool { return placeOf(out[i]).before(placeOf(out[j])) })
	limit := int(limes.PageLimit(req.Limit))
	if len(out) <= limit {
		return limes.Page[meter.Reservation]{Items: out}, nil
	}
	out = out[:limit]
	return limes.Page[meter.Reservation]{Items: out, Next: placeOf(out[len(out)-1]).String()}, nil
}

// reservationPlace is a reservation's place in ListReservations' order, and
// memstore's cursor: its expiry and id.
type reservationPlace struct {
	expires time.Time
	id      uuid.UUID
}

func placeOf(r meter.Reservation) reservationPlace { return reservationPlace{r.ExpiresAt, r.ID} }

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

// Release ends a reservation without charging. Idempotent; a hold of
// another capability or tenant, or of other copies, is left held.
func (s *UsageStore) Release(_ context.Context, req meter.ReleaseRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.holds[req.ReservationID]
	if !ok || h.capID != req.Subject.CapabilityID() || h.tenantID != req.Subject.TenantID() || !sameCopies(h.copies, req.Subject.Copies()) {
		return nil
	}
	s.applyHoldLocked(h, -1)
	delete(s.holds, req.ReservationID)
	return nil
}

// ReleaseExpired releases every hold past its expiry.
func (s *UsageStore) ReleaseExpired(_ context.Context, req meter.ReleaseExpiredRequest) (meter.ReleaseExpiredResult, error) {
	if err := req.Validate(); err != nil {
		return meter.ReleaseExpiredResult{}, err
	}
	limit := meter.HousekeepingLimit(req.Limit)
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
	return meter.ReleaseExpiredResult{Released: int64(len(due))}, nil
}

// Refund returns spend from one ledger entry to the counters it debited.
func (s *UsageStore) Refund(_ context.Context, req meter.RefundRequest) (meter.RefundReceipt, error) {
	if err := req.Validate(); err != nil {
		return meter.RefundReceipt{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	i, ok := s.byCharge[req.ChargeID]
	if !ok {
		return meter.RefundReceipt{}, meter.ErrChargeNotFound
	}
	e := &s.ledger[i]
	receipt := meter.RefundReceipt{ChargeID: e.ID, TenantID: e.TenantID, UnitCode: e.UnitCode}
	remaining := e.Amount - e.RefundedAmount
	amount := req.Amount
	if req.Remaining {
		amount = remaining
	}
	if amount > remaining {
		return meter.RefundReceipt{}, fmt.Errorf("%w: %v requested, %v of %v left",
			meter.ErrRefundExceedsCharge, amount, remaining, e.Amount)
	}
	if amount == 0 {
		return receipt, nil
	}
	for _, id := range append([]uuid.UUID{e.CapabilityID}, e.Ancestors...) {
		if u, ok := s.usage[id]; ok {
			u.SpentAmount = max0(u.SpentAmount - amount)
			s.putUsageLocked(id, e.TenantID, u)
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
		key := copyKeyOf(e.CapabilityID, id)
		if cc, ok := s.copies[key]; ok {
			cc.spent = max0(cc.spent - amount)
			s.putCopyLocked(key, cc)
		}
	}
	e.RefundedAmount += amount
	receipt.Amount = amount
	return receipt, nil
}

// GetUsage reads one capability's counters.
func (s *UsageStore) GetUsage(_ context.Context, capID uuid.UUID) (meter.Usage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.usage[capID]
	if !ok {
		return meter.Usage{}, meter.ErrUsageNotFound
	}
	return u, nil
}

// CopyUsage implements meter.CopyUsageReader.
func (s *UsageStore) CopyUsage(_ context.Context, req meter.CopyUsageRequest) (meter.CopyUsageResult, error) {
	if err := req.Validate(); err != nil {
		return meter.CopyUsageResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []meter.CopyUsage
	for _, id := range req.RevocationIDs {
		if c, ok := s.copies[copyKeyOf(req.CapabilityID, id)]; ok {
			out = append(out, meter.CopyUsage{
				RevocationID: slices.Clone(id), CapabilityID: req.CapabilityID,
				RequestCount: c.requests, SpentAmount: c.spent, ReservedAmount: c.reserved,
			})
		}
	}
	return meter.CopyUsageResult{Copies: out}, nil
}

// GetTenantBudget reads one tenant's aggregate and ceiling.
func (s *UsageStore) GetTenantBudget(_ context.Context, tenantID uuid.UUID) (meter.TenantBudget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.budgets[tenantID]
	if !ok {
		return meter.TenantBudget{}, meter.ErrTenantBudgetNotFound
	}
	return b, nil
}

// SetTenantBudget sets a tenant's ceiling under the request's version guard.
func (s *UsageStore) SetTenantBudget(_ context.Context, args meter.SetTenantBudgetRequest) (meter.TenantBudget, error) {
	if err := args.Validate(); err != nil {
		return meter.TenantBudget{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.notErasedLocked(args.TenantID); err != nil {
		return meter.TenantBudget{}, err
	}

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
		return meter.TenantBudget{}, meter.ErrTenantBudgetVersionMismatch
	}

	// A ceiling that names no unit — the zero one — keeps the tenant's.
	unit := args.Ceiling.UnitCode
	if unit == "" {
		unit = b.Ceiling.UnitCode
	}
	// A reset closes the spend, never the holds still open in the old unit.
	if have := b.Ceiling.UnitCode; exists && have != "" && unit != have &&
		(b.ReservedAmount > 0 || (b.SpentAmount > 0 && !args.ResetSpend)) {
		return meter.TenantBudget{}, fmt.Errorf("%w: tenant %s has %s counted in %s",
			limes.ErrUnitCodeMismatch, args.TenantID, b.SpentAmount+b.ReservedAmount, have)
	}

	b.TenantID = args.TenantID
	b.Ceiling = args.Ceiling
	b.Ceiling.UnitCode = unit
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

// ListTenantBudgets pages the tenants' aggregates, most at risk first.
func (s *UsageStore) ListTenantBudgets(_ context.Context, args meter.ListTenantBudgetsRequest) (limes.Page[meter.TenantBudget], error) {
	if err := args.Validate(); err != nil {
		return limes.Page[meter.TenantBudget]{}, err
	}
	var after *budgetRow
	if args.Cursor != "" {
		c, err := decodeTenantBudgetCursor(args.Cursor)
		if err != nil {
			return limes.Page[meter.TenantBudget]{}, err
		}
		after = &budgetRow{class: c.class, spent: c.spent, max: c.max, tenant: c.tenantID}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	rows := make([]budgetRow, 0, len(s.budgets))
	for _, b := range s.budgets {
		row := budgetRow{class: classOf(b.Ceiling), tenant: b.TenantID, budget: b}
		if row.class == classBounded {
			row.spent, row.max = b.SpentAmount, b.Ceiling.Max
		}
		if !listed(args, row) {
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
		next = tenantBudgetCursor{class: last.class, spent: last.spent, max: last.max, tenantID: last.tenant}.encode()
	}
	out := make([]meter.TenantBudget, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.budget)
	}
	return limes.Page[meter.TenantBudget]{Items: out, Next: next}, nil
}

// budgetRow is a tenant budget in ListTenantBudgets' order.
//
// A bounded row ranks by spent / max, compared exactly as a fraction — the
// unclamped utilization — rather than as a float, which could tie two
// different spends or order them backwards.
type budgetRow struct {
	class ceilingClass
	// spent and max are the utilization's numerator and denominator; zero
	// outside classBounded.
	spent, max limes.Nanos
	tenant     uuid.UUID
	budget     meter.TenantBudget
}

// compareUtilization compares a's spent / max with b's exactly, by cross
// multiplication in big integers: -1, 0 or +1.
func compareUtilization(a, b budgetRow) int {
	left := new(big.Int).Mul(big.NewInt(int64(a.spent)), big.NewInt(int64(b.max)))
	right := new(big.Int).Mul(big.NewInt(int64(b.spent)), big.NewInt(int64(a.max)))
	return left.Cmp(right)
}

// listed reports whether args' filter keeps row. A ceiling of 0 is spent
// through, at every threshold; an unlimited one is at none.
func listed(args meter.ListTenantBudgetsRequest, row budgetRow) bool {
	switch args.Filter {
	case meter.TenantBudgetFilterUnspecified, meter.TenantBudgetFilterAll:
		return true
	case meter.TenantBudgetFilterUnlimited:
		return row.class == classUnlimited
	case meter.TenantBudgetFilterAtOrPast:
		switch row.class {
		case classZero:
			return true
		case classBounded:
			return atOrPast(row.spent, row.max, args.ThresholdBasisPoints)
		case classUnlimited:
			return false
		}
	}
	return false // Validate refused any other filter
}

// atOrPast reports whether spent is at least basisPoints of ceiling, exactly:
// spent × BasisPointsPerWhole ≥ basisPoints × ceiling in integers, where a
// float quotient would put 29 of 100 just under 29%. The products pass
// int64, hence big.Int.
func atOrPast(spent, ceiling limes.Nanos, basisPoints int) bool {
	used := new(big.Int).Mul(big.NewInt(int64(spent)), big.NewInt(meter.BasisPointsPerWhole))
	threshold := new(big.Int).Mul(big.NewInt(int64(ceiling)), big.NewInt(int64(basisPoints)))
	return used.Cmp(threshold) >= 0
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
	if r.class == classBounded {
		if c := compareUtilization(r, o); c != 0 {
			return c > 0
		}
	}
	return uuidLess(r.tenant, o.tenant)
}

// releaseHoldsLocked releases every hold of a capability gone reports as
// gone. s.mu must be held.
func (s *UsageStore) releaseHoldsLocked(gone func(capID uuid.UUID) bool) {
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
func (s *UsageStore) DeleteUsage(_ context.Context, req meter.DeleteUsageRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	capID := req.CapabilityID
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseHoldsLocked(func(id uuid.UUID) bool { return id == capID })
	s.dropUsageLocked(capID)
	s.dropCopiesLocked(func(id uuid.UUID) bool { return id == capID })
	s.forgetAncestorLocked(func(id uuid.UUID) bool { return id == capID })
	return nil
}

// forgetAncestorLocked drops every id gone reports from the ancestors of the
// holds that remain: a descendant's hold still names a dropped row's
// capability, and releasing or settling it later must not write that row
// back. s.mu must be held.
func (s *UsageStore) forgetAncestorLocked(gone func(uuid.UUID) bool) {
	for id, h := range s.holds {
		if slices.ContainsFunc(h.ancestors, gone) {
			h.ancestors = slices.DeleteFunc(slices.Clone(h.ancestors), gone)
			s.holds[id] = h
		}
	}
}

// EraseTenantUsage implements meter.TenantUsageEraser: it drops the
// tenant's holds, counters, copies' counters, ledger entries and aggregate.
func (s *UsageStore) EraseTenantUsage(_ context.Context, req meter.EraseTenantUsageRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	tenant := req.TenantID
	s.mu.Lock()
	defer s.mu.Unlock()
	mine := func(capID uuid.UUID) bool { return s.tenantOf[capID] == tenant }
	maps.DeleteFunc(s.holds, func(_ uuid.UUID, h hold) bool { return h.tenantID == tenant })
	s.dropCopiesLocked(mine)
	for id := range s.usage {
		if mine(id) {
			s.dropUsageLocked(id)
		}
	}
	ledger := slices.DeleteFunc(s.ledger, func(e ledgerEntry) bool { return e.TenantID == tenant })
	s.ledger = nil
	clear(s.byCharge)
	clear(s.byRef)
	clear(s.byReservation)
	for _, e := range ledger {
		s.appendLocked(e)
	}
	delete(s.budgets, tenant)
	delete(s.periods, tenant)
	s.erased[tenant] = struct{}{}
	return nil
}

// notErasedLocked refuses a tenant EraseTenantUsage removed. s.mu must be
// held.
func (s *UsageStore) notErasedLocked(tenant uuid.UUID) error {
	if _, ok := s.erased[tenant]; ok {
		return fmt.Errorf("%w: %s", limes.ErrTenantErased, tenant)
	}
	return nil
}

// PurgeOrphans drops the counters of capabilities no longer on record, a
// batch at a time, releasing their holds first.
func (s *UsageStore) PurgeOrphans(_ context.Context, req meter.PurgeOrphansRequest) (meter.PurgeOrphansResult, error) {
	if err := req.Validate(); err != nil {
		return meter.PurgeOrphansResult{}, err
	}
	limit := meter.HousekeepingLimit(req.Limit)
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
		s.dropUsageLocked(id)
	}
	s.forgetAncestorLocked(func(id uuid.UUID) bool { return batch[id] })
	s.dropCopiesLocked(func(id uuid.UUID) bool { return batch[id] })
	// An erased tenant with no record left has no capability to meter.
	maps.DeleteFunc(s.erased, func(tenant uuid.UUID, _ struct{}) bool { return !s.records.holdsTenant(tenant) })
	return meter.PurgeOrphansResult{Purged: int64(len(ids))}, nil
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
	_ limes.TenantRecordEraser = (*Store)(nil)
	_ limes.Store              = (*Store)(nil)
	_ limes.IssuanceStore      = (*Store)(nil)
	_ meter.UsageStore         = (*UsageStore)(nil)
	_ meter.TenantUsageEraser  = (*UsageStore)(nil)
)

var _ meter.CopyUsageReader = (*UsageStore)(nil)
