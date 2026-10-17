// Package storetest checks a limes.Store — and, when it is one, a
// limes.BiscuitRevocationStore — against the module's contract, the way
// testing/fstest checks an fs.FS. Run it from the store's own tests:
//
//	func TestStoreContract(t *testing.T) {
//		storetest.Run(t, func(t *testing.T) storetest.Env { … })
//	}
//
// The checks cover what every caller relies on: a record reads back as it was
// written and is no caller's to change, a duplicate or unknown id is refused
// with the module's sentinel, Insert refuses a lineage it could not keep whole
// — a child past MaxDelegationDepth among it — and a tenant it cannot mint
// for, revocation reaches every descendant down to MaxDelegationDepth and outlasts time passing, purging drops a record with
// its revocation entries once it has been expired long enough — in batches,
// never before its children and never inside a verifier's leeway — listing
// pages in a fixed order and keeps tenants apart, and a Biscuit copy revokes
// on its own. When the store implements limes.TenantRecordEraser, erasing a
// tenant keeps what a verifier still needs; when Env.ScopesTenants is set, a
// call under another tenant's scope is refused with limes.ErrScopeMismatch,
// and when Env.ReadOnlyCtx is set, every write under a read-only scope is.
// When Env.Census is set, a census counts each record under one disposition —
// revoked, through an ancestor too, over expired over active — by tenant and
// by principal type, a page at a time. A check that does not finish within Env.CheckTimeout stops the
// test binary with every goroutine's stack, naming the check.
package storetest

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/fields"
	"github.com/oleg-tkachuk/limes/internal/link"
	"github.com/oleg-tkachuk/limes/internal/watchdog"
)

// DefaultCheckTimeout bounds a check whose Env sets no CheckTimeout.
const DefaultCheckTimeout = watchdog.DefaultLimit

// Env is one store under test, fresh for each check. Its fields mirror
// metertest.Env, so one setup can serve both suites.
type Env struct {
	// Ctx is the context every call for Tenant runs under — scoped to it, if
	// the store needs that. A write for a tenant from NewTenant runs under
	// TenantCtx, when the Env sets it.
	Ctx context.Context
	// Store is the store under test, with nothing on record.
	Store limes.Store
	// Tenant is a tenant the store can record capabilities for.
	Tenant uuid.UUID
	// NewTenant creates another tenant the store can record capabilities
	// for, for the checks that need more than one. Optional: without it, a
	// random id, which a store that refers to its tenants refuses.
	NewTenant func() (uuid.UUID, error)
	// TenantCtx is the context a write for tenant — one NewTenant created —
	// runs under, when the store scopes each tenant's rows; Tenant's own
	// calls run under Ctx. Optional: without it, Ctx.
	TenantCtx func(tenant uuid.UUID) context.Context
	// ScopesTenants reports that the store keeps each tenant's rows apart
	// by the call's context, TenantCtx giving each tenant's: a call whose
	// request names one tenant, made under another tenant's context, is
	// refused with limes.ErrScopeMismatch and changes nothing. Optional:
	// without it, the checks of that are skipped.
	ScopesTenants bool
	// ReadOnlyCtx is a context scoped to reading every tenant's rows and
	// writing none, as the store's consumer scopes it — by the store's own
	// context or by a scope of the consumer's: Tenant's records read under
	// it, and every call that writes is refused with
	// limes.ErrScopeMismatch and changes nothing. Optional: without it, the
	// checks of that are skipped.
	ReadOnlyCtx context.Context
	// Now reads the store's clock, which stands still while a check runs
	// but for Advance. Optional: without it, the checks at an expiry's exact
	// instant are skipped, and the checks read the wall clock.
	Now func() time.Time
	// Advance moves the store's clock forward by d. Optional: without it,
	// the checks of what time passing must leave alone are skipped.
	Advance func(d time.Duration)
	// DeleteTenant moves a tenant NewTenant created to the trash, for a
	// store that knows its tenants: it then refuses an id it never created
	// as ErrUnknownTenant and one in the trash as ErrTenantDeleted.
	// Optional: without it, those checks are skipped.
	DeleteTenant func(tenant uuid.UUID) error
	// CheckTimeout bounds one check, setup aside: past it the suite panics
	// with every goroutine's stack, naming the check, rather than leave a
	// deadlocked store to go test's -timeout. Optional: without it,
	// DefaultCheckTimeout.
	CheckTimeout time.Duration
	// Census counts the store's records. Optional: without it, or without
	// Now, the checks of limes.RecordCensus are skipped. A count across
	// every tenant runs under ReadOnlyCtx when the Env sets it, else Ctx.
	Census limes.RecordCensus
}

// newTenant is another tenant, from NewTenant when the Env has one.
func (e Env) newTenant() (uuid.UUID, error) {
	if e.NewTenant == nil {
		return uuid.New(), nil
	}
	return e.NewTenant()
}

// tenantCtx is the context a write for tenant runs under.
func (e Env) tenantCtx(tenant uuid.UUID) context.Context {
	if e.TenantCtx == nil {
		return e.Ctx
	}
	return e.TenantCtx(tenant)
}

const (
	// ttl is how long a live capability of these checks lives; expiredAgo is
	// how long ago an expired one expired, and purgeGrace sits between, so
	// Purge takes the expired one and leaves the live one.
	ttl        = time.Hour
	expiredAgo = 2 * time.Hour
	purgeGrace = time.Hour
	// pageSize and listed are the page size and the capability count of the
	// listing check: three pages, the last one short.
	pageSize = 2
	listed   = 5
	// timestampResolution is the finest a timestamp column keeps.
	timestampResolution = time.Microsecond
)

// issuedBy is the principal every capability of these checks is issued by.
var issuedBy = limes.Principal{Type: limes.PrincipalTypeService, Subject: "svc:storetest"}

type fixture struct {
	Env
	now time.Time
}

// newCapability is a capability with every field set, so a store that drops
// one fails the round trip.
func (f fixture) newCapability(subject string, parent uuid.UUID, expires time.Time) limes.Capability {
	return limes.Capability{
		ID:     uuid.New(),
		Issuer: "storetest",
		Subject: limes.Principal{
			Type: limes.PrincipalTypeAgent, TenantID: f.Tenant, Subject: subject,
			Agent: &limes.AgentPrincipal{
				AgentType: "worker", AgentVersion: "1", RunID: uuid.New(), ParentAgentID: uuid.New(), Model: "m", MCPClient: "client",
			},
		},
		Audience: []string{"data", "mcp"},
		Caveats: limes.Caveats{
			Ops:                    []limes.Op{limes.OpGet, "tool:search"},
			ResourcePrefixes:       []string{"corpus/"},
			ResourceURIs:           []string{"corpus/index.json"},
			Requests:               limes.RequestCeiling{Max: 7},
			Budget:                 limes.BudgetCeiling{Max: 3 * limes.NanosPerUnit / 2, UnitCode: "EUR"}, // 1.5 EUR
			AllowTaintedRead:       true,
			IdempotencyKeyRequired: true,
			SourceIPCIDR:           []string{"10.0.0.0/8"},
		},
		IssuedAt:        f.now,
		NotBefore:       f.now,
		ExpiresAt:       expires,
		ParentID:        parent,
		Generation:      1,
		ConfirmationJKT: "jkt",
	}
}

// issuer is a principal with every field set, so a store that drops one
// from Record.IssuedBy fails the round trip.
func (f fixture) issuer() limes.Principal {
	return limes.Principal{
		Type: limes.PrincipalTypeAgent, TenantID: f.Tenant, Subject: "agent:issuer",
		Agent: &limes.AgentPrincipal{
			AgentType: "planner", AgentVersion: "2", RunID: uuid.New(), ParentAgentID: uuid.New(), Model: "issuer-model", MCPClient: "issuer-client",
		},
	}
}

func (f fixture) insert(t *testing.T, c limes.Capability) limes.Capability {
	t.Helper()
	if err := f.Store.Insert(f.Ctx, limes.InsertRequest{Capability: c, IssuedBy: issuedBy}); err != nil {
		t.Fatalf("insert %s: %v", c.ID, err)
	}
	return c
}

func (f fixture) live(t *testing.T, subject string, parent uuid.UUID) limes.Capability {
	t.Helper()
	return f.insert(t, f.newCapability(subject, parent, f.now.Add(ttl)))
}

// get is the capability on record under id.
func (f fixture) get(id uuid.UUID) (limes.Capability, error) {
	r, err := f.Store.GetRecord(f.Ctx, id)
	return r.Capability, err
}

func (f fixture) revoked(t *testing.T, id uuid.UUID) bool {
	t.Helper()
	r, err := f.Store.IsRevoked(f.Ctx, id)
	if err != nil {
		t.Fatalf("IsRevoked %s: %v", id, err)
	}
	return r
}

func (f fixture) revoke(t *testing.T, id uuid.UUID, cascade bool) {
	t.Helper()
	if err := f.Store.Revoke(f.Ctx, limes.RevokeRequest{CapabilityID: id, Note: "test", Actor: "storetest", CascadeChildren: cascade}); err != nil {
		t.Fatalf("revoke %s: %v", id, err)
	}
}

// Run runs every check as a subtest, each on a fresh Env from setup.
func Run(t *testing.T, setup func(t *testing.T) Env) {
	t.Helper()
	checks := []struct {
		name string
		run  func(*testing.T, fixture)
	}{
		{"InsertReadsBack", checkRoundTrip},
		{"InsertRefusesADuplicate", checkDuplicate},
		{"ARecordIsNoCallersToChange", checkRecordIsItsOwn},
		{"InsertDropsTheCopiesAVerifierSets", checkInsertDropsCopies},
		{"GetRecordReadsBackIssuanceAndRevocation", checkRecord},
		{"UnknownIDs", checkUnknown},
		{"RevokeIsIdempotentAndReachesDescendants", checkRevoke},
		{"CascadeRecordsEveryDescendant", checkCascade},
		{"RevocationReachesEveryGeneration", checkDeepChain},
		{"RevocationReachesTheDeepestChainAndInsertRefusesADeeperOne", checkMaxDepth},
		{"CascadeRecordsTheDeepestChain", checkMaxDepthCascade},
		{"PurgeDropsExpiredRecordsAndTheirEntries", checkPurge},
		{"PurgeWorksInBatches", checkPurgeBatches},
		{"PurgeTakesTheDefaultBatchWhenAskedForNone", checkPurgeDefaultLimit},
		{"ARevocationOutlastsTimePassing", checkRevocationOutlastsTime},
		{"ListByPrincipalClampsItsLimit", checkListLimits},
		{"ListByPrincipalEndsOnAFullLastPage", checkListFullLastPage},
		{"PurgeKeepsARevocationAVerifierMayStillNeed", checkPurgeRetention},
		{"PurgeNeverLeavesAChildWithoutItsParent", checkPurgeKeepsLineage},
		{"InsertRefusesABrokenLineage", checkInsertKeepsLineage},
		{"ListByPrincipalPages", checkListPages},
		{"ListByPrincipalFilters", checkListFilters},
		{"ListByPrincipalExpiresAtTheInstant", checkListExpiryInstant},
		{"ListByPrincipalRefusesAMalformedRequest", checkListMalformed},
		{"ListByPrincipalKeepsTenantsApart", checkListTenants},
		{"InsertRefusesATenantItCannotMintFor", checkInsertTenants},
		{"BiscuitRevocation", checkBiscuit},
		{"ACallScopedToAnotherTenantIsRefused", checkScopeMismatch},
		{"AReadOnlyScopeWritesNothing", checkReadOnlyScope},
		{"CensusCountsEachRecordOnce", checkCensus},
		{"CensusPagesAndRefusesAMalformedRequest", checkCensusPages},
		{"EraseTenantRecordsKeepsWhatAVerifierStillNeeds", checkEraseTenant},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			env := setup(t)
			now := time.Now().UTC().Truncate(timestampResolution)
			if env.Now != nil {
				now = env.Now()
			}
			watchdog.Run(c.name, env.CheckTimeout, func() { c.run(t, fixture{Env: env, now: now}) })
		})
	}
}

func checkRoundTrip(t *testing.T, f fixture) {
	parent := f.live(t, "agent:parent", uuid.Nil)
	want := f.live(t, "agent:child", parent.ID)
	got, err := f.get(want.ID)
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if diff := diffCapabilities(got, want); diff != "" {
		t.Fatalf("Get read back a different capability: %s", diff)
	}
	// Each ceiling lifted in turn: a store that drops either Unlimited reads
	// a ceiling of 0 back, and a capability that allows nothing.
	for name, lift := range map[string]func(*limes.Caveats){
		"requests unlimited": func(c *limes.Caveats) { c.Requests = limes.RequestCeiling{Unlimited: true} },
		"budget unlimited":   func(c *limes.Caveats) { c.Budget = limes.BudgetCeiling{Unlimited: true, UnitCode: c.Budget.UnitCode} },
	} {
		c := f.newCapability("agent:unlimited", uuid.Nil, f.now.Add(ttl))
		lift(&c.Caveats)
		f.insert(t, c)
		got, err := f.get(c.ID)
		if err != nil {
			t.Fatalf("%s: GetRecord: %v", name, err)
		}
		if diff := diffCapabilities(got, c); diff != "" {
			t.Errorf("%s: Get read back a different capability: %s", name, diff)
		}
	}
}

func checkRecord(t *testing.T, f fixture) {
	parent := f.live(t, "agent:parent", uuid.Nil)
	child := f.live(t, "agent:child", parent.ID)
	cascadeRoot := f.live(t, "agent:cascade", uuid.Nil)
	cascadeChild := f.live(t, "agent:cascade-child", cascadeRoot.ID)
	record := func(id uuid.UUID) limes.Record {
		t.Helper()
		r, err := f.Store.GetRecord(f.Ctx, id)
		if err != nil {
			t.Fatalf("GetRecord %s: %v", id, err)
		}
		return r
	}

	issuer := f.issuer()
	issued := f.newCapability("agent:issued", uuid.Nil, f.now.Add(ttl))
	if err := f.Store.Insert(f.Ctx, limes.InsertRequest{Capability: issued, IssuedBy: issuer}); err != nil {
		t.Fatalf("insert %s: %v", issued.ID, err)
	}
	r := record(issued.ID)
	if diff := diffCapabilities(r.Capability, issued); diff != "" {
		t.Errorf("GetRecord read back a different capability: %s", diff)
	}
	if !reflect.DeepEqual(r.IssuedBy, issuer) || r.Revocation != nil {
		t.Errorf("before any revoke: issued by %+v, revocation %+v; want %+v and none", r.IssuedBy, r.Revocation, issuer)
	}

	f.revoke(t, parent.ID, false)
	if err := f.Store.Revoke(f.Ctx, limes.RevokeRequest{CapabilityID: parent.ID, Note: "again", Actor: "someone-else"}); err != nil {
		t.Fatal(err)
	}
	rev := record(parent.ID).Revocation
	if rev == nil || rev.Note != "test" || rev.Actor != "storetest" || rev.Cascaded || rev.RevokedAt.IsZero() {
		t.Errorf("revocation = %+v; want the first revoke's reason and actor, not cascading, with a time", rev)
	}
	if rev := record(child.ID).Revocation; rev != nil {
		t.Errorf("a child stopped through its parent has an entry of its own: %+v", rev)
	}
	f.revoke(t, cascadeRoot.ID, true)
	for _, id := range []uuid.UUID{cascadeRoot.ID, cascadeChild.ID} {
		rev := record(id).Revocation
		if rev == nil || !rev.Cascaded {
			t.Errorf("%s after a cascading revoke: revocation %+v, want a cascading entry", id, rev)
			continue
		}
		// Every field of a cascading entry is set, so one the store does not
		// keep reads back as unset.
		if unset := fields.Unset(*rev); len(unset) > 0 {
			t.Errorf("%s: revocation %+v reads back with %v unset", id, rev, unset)
		}
	}
	for _, id := range []uuid.UUID{uuid.New(), uuid.Nil} {
		if _, err := f.Store.GetRecord(f.Ctx, id); !errors.Is(err, limes.ErrNotFound) {
			t.Errorf("GetRecord %s: err = %v, want ErrNotFound", id, err)
		}
	}
}

func checkDuplicate(t *testing.T, f fixture) {
	c := f.live(t, "agent:a", uuid.Nil)
	again := c
	again.Subject.Subject = "agent:b"
	if err := f.Store.Insert(f.Ctx, limes.InsertRequest{Capability: again, IssuedBy: issuedBy}); !errors.Is(err, limes.ErrAlreadyExists) {
		t.Fatalf("second insert of %s: err = %v, want ErrAlreadyExists", c.ID, err)
	}
	if got, err := f.get(c.ID); err != nil || got.Subject.Subject != "agent:a" {
		t.Fatalf("after a refused insert Get = %+v, %v; want the first record", got, err)
	}
}

func checkUnknown(t *testing.T, f fixture) {
	for _, id := range []uuid.UUID{uuid.New(), uuid.Nil} {
		if _, err := f.get(id); !errors.Is(err, limes.ErrNotFound) {
			t.Errorf("Get %s: err = %v, want ErrNotFound", id, err)
		}
		if err := f.Store.Revoke(f.Ctx, limes.RevokeRequest{CapabilityID: id}); !errors.Is(err, limes.ErrNotFound) {
			t.Errorf("Revoke %s: err = %v, want ErrNotFound", id, err)
		}
		if f.revoked(t, id) {
			t.Errorf("IsRevoked %s = true for an id not on record", id)
		}
	}
}

func checkRevoke(t *testing.T, f fixture) {
	parent := f.live(t, "agent:parent", uuid.Nil)
	child := f.live(t, "agent:child", parent.ID)
	other := f.live(t, "agent:other", uuid.Nil)
	f.revoke(t, parent.ID, false)
	f.revoke(t, parent.ID, false) // idempotent
	if !f.revoked(t, parent.ID) || !f.revoked(t, child.ID) {
		t.Error("revoking a parent must stop it and its child")
	}
	if f.revoked(t, other.ID) {
		t.Error("revoking one capability stopped an unrelated one")
	}
}

// Cascade writes an entry for each descendant, which ListByPrincipal shows:
// it hides a capability revoked itself, not one revoked through an ancestor.
func checkCascade(t *testing.T, f fixture) {
	const subject = "agent:tree"
	plainRoot := f.live(t, "agent:plain", uuid.Nil)
	plainChild := f.live(t, subject, plainRoot.ID)
	cascadeRoot := f.live(t, "agent:cascade", uuid.Nil)
	cascadeChild := f.live(t, subject, cascadeRoot.ID)
	f.revoke(t, plainRoot.ID, false)
	f.revoke(t, cascadeRoot.ID, true)

	got := f.listAll(t, limes.ListByPrincipalRequest{
		TenantID: f.Tenant, PrincipalType: limes.PrincipalTypeAgent, Subject: subject,
	})
	if !slices.Contains(got, plainChild.ID) || slices.Contains(got, cascadeChild.ID) {
		t.Fatalf("listing hides %v; want only the child revoked by cascade (%s) hidden", got, cascadeChild.ID)
	}
}

// checkDeepChain holds a store to the whole delegation chain, not its first
// link: a store that joins a capability to its parent once, rather than
// walking to the root, passes the one-level checks above and misses a
// grandchild. A cascade must also stay below where it started.
func checkDeepChain(t *testing.T, f fixture) {
	root := f.live(t, "agent:root", uuid.Nil)
	mid := f.live(t, "agent:mid", root.ID)
	leaf := f.live(t, "agent:leaf", mid.ID)
	f.revoke(t, root.ID, false)
	if !f.revoked(t, leaf.ID) {
		t.Error("revoking a root must stop its grandchild")
	}

	top := f.live(t, "agent:top", uuid.Nil)
	from := f.live(t, "agent:from", top.ID)
	sibling := f.live(t, "agent:sibling", top.ID)
	below := f.live(t, "agent:below", from.ID)
	under := f.live(t, "agent:under", below.ID)
	f.revoke(t, from.ID, true)
	for _, c := range []limes.Capability{from, below, under} {
		rec, err := f.Store.GetRecord(f.Ctx, c.ID)
		if err != nil || rec.Revocation == nil {
			t.Errorf("GetRecord(%s) = %+v, %v; want a revocation entry written by the cascade", c.Subject.Subject, rec.Revocation, err)
		}
	}
	for _, c := range []limes.Capability{top, sibling} {
		if f.revoked(t, c.ID) {
			t.Errorf("a cascade from %s reached %s, which is not below it", from.Subject.Subject, c.Subject.Subject)
		}
	}
}

func checkPurge(t *testing.T, f fixture) {
	expired := f.insert(t, f.newCapability("agent:old", uuid.Nil, f.now.Add(-expiredAgo)))
	unrevoked := f.insert(t, f.newCapability("agent:older", uuid.Nil, f.now.Add(-expiredAgo)))
	live := f.live(t, "agent:new", uuid.Nil)
	// Expired past MinPurgeRetention but within the retention asked for: kept.
	recent := f.insert(t, f.newCapability("agent:recent", uuid.Nil, f.now.Add(-purgeGrace/2)))
	f.revoke(t, expired.ID, false)
	f.revoke(t, live.ID, false)
	got, err := f.Store.Purge(f.Ctx, limes.PurgeRequest{ExpiredFor: purgeGrace})
	const want = 2 // the two expired records
	if err != nil || got.Purged != want {
		t.Fatalf("Purge = %+v, %v; want %d records purged", got, err, want)
	}
	for _, id := range []uuid.UUID{expired.ID, unrevoked.ID} {
		if _, err := f.get(id); !errors.Is(err, limes.ErrNotFound) {
			t.Errorf("an expired record outlived Purge: GetRecord = %v", err)
		}
	}
	if f.revoked(t, expired.ID) {
		t.Error("a purged capability is still revoked")
	}
	if _, err := f.get(live.ID); err != nil || !f.revoked(t, live.ID) {
		t.Errorf("Purge must keep a live record and its revocation: %v", err)
	}
	if _, err := f.get(recent.ID); err != nil {
		t.Errorf("Purge removed a record expired for less than ExpiredFor: %v", err)
	}
}

// Purge works in batches of Limit, so a housekeeping worker loops while a
// batch comes back full; a negative bound is the request's error.
func checkPurgeBatches(t *testing.T, f fixture) {
	const total, batch = 3, 2
	for range total {
		f.insert(t, f.newCapability("agent:old", uuid.Nil, f.now.Add(-expiredAgo)))
	}
	first, err := f.Store.Purge(f.Ctx, limes.PurgeRequest{ExpiredFor: purgeGrace, Limit: batch})
	if err != nil || first.Purged != batch {
		t.Fatalf("first batch = %+v, %v; want %d", first, err, batch)
	}
	second, err := f.Store.Purge(f.Ctx, limes.PurgeRequest{ExpiredFor: purgeGrace, Limit: batch})
	if err != nil || second.Purged != total-batch {
		t.Fatalf("second batch = %+v, %v; want %d", second, err, total-batch)
	}
	for name, req := range map[string]limes.PurgeRequest{
		"a negative limit":              {Limit: -1},
		"a negative retention":          {ExpiredFor: -time.Second},
		"a retention below the minimum": {ExpiredFor: limes.MinPurgeRetention - time.Second},
	} {
		if _, err := f.Store.Purge(f.Ctx, req); !errors.Is(err, limes.ErrInvalidRequest) {
			t.Errorf("Purge with %s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
}

// checkPurgeDefaultLimit holds Purge to DefaultHousekeepingLimit when no
// limit is asked for, rather than everything due in one go.
func checkPurgeDefaultLimit(t *testing.T, f fixture) {
	for range limes.DefaultHousekeepingLimit + 1 {
		f.insert(t, f.newCapability("agent:old", uuid.Nil, f.now.Add(-expiredAgo)))
	}
	got, err := f.Store.Purge(f.Ctx, limes.PurgeRequest{ExpiredFor: purgeGrace})
	if err != nil || got.Purged != limes.DefaultHousekeepingLimit {
		t.Errorf("Purge with no limit = %+v, %v; want DefaultHousekeepingLimit (%d)", got, err, limes.DefaultHousekeepingLimit)
	}
}

// checkRevocationOutlastsTime holds a store to keeping the revocation of a
// capability still live however long ago it was revoked: Purge, or
// anything else that ages rows out, must not take it. It needs Env.Advance.
func checkRevocationOutlastsTime(t *testing.T, f fixture) {
	if f.Advance == nil {
		t.Skip("Env.Advance is not set")
	}
	// Live well past the time that passes: several retentions.
	const lives, passes = 4 * limes.MinPurgeRetention, 2 * limes.MinPurgeRetention
	c := f.insert(t, f.newCapability("agent:long-lived", uuid.Nil, f.now.Add(lives)))
	f.revoke(t, c.ID, false)
	f.Advance(passes)
	if _, err := f.Store.Purge(f.Ctx, limes.PurgeRequest{}); err != nil {
		t.Fatal(err)
	}
	if !f.revoked(t, c.ID) {
		t.Errorf("a live capability revoked %s ago is no longer revoked", passes)
	}
}

// checkListLimits holds ListByPrincipal to its page sizes: DefaultListLimit
// when none is asked for, and never more than MaxListLimit.
func checkListLimits(t *testing.T, f fixture) {
	const subject = "agent:many"
	for range limes.MaxListLimit + 1 {
		f.live(t, subject, uuid.Nil)
	}
	for name, tc := range map[string]struct{ limit, want int }{
		"no limit":      {0, limes.DefaultListLimit},
		"past the most": {2 * limes.MaxListLimit, limes.MaxListLimit},
	} {
		page, err := f.Store.ListByPrincipal(f.Ctx, limes.ListByPrincipalRequest{
			TenantID: f.Tenant, PrincipalType: limes.PrincipalTypeAgent, Subject: subject, Limit: tc.limit,
		})
		if err != nil || len(page.Items) != tc.want || page.Next == "" {
			t.Errorf("%s: %d items, next %q, %v; want %d and a next page", name, len(page.Items), page.Next, err, tc.want)
		}
	}
}

// checkListFullLastPage holds ListByPrincipal to an empty cursor on the last
// page, a page filled exactly included: a cursor there costs the caller a
// request that can only come back empty.
func checkListFullLastPage(t *testing.T, f fixture) {
	const subject = "agent:exact"
	for range pageSize {
		f.live(t, subject, uuid.Nil)
	}
	page, err := f.Store.ListByPrincipal(f.Ctx, limes.ListByPrincipalRequest{
		TenantID: f.Tenant, PrincipalType: limes.PrincipalTypeAgent, Subject: subject, Limit: pageSize,
	})
	if err != nil || len(page.Items) != pageSize || page.Next != "" {
		t.Errorf("an exactly full last page: %d items, next %q, %v; want %d and no next page", len(page.Items), page.Next, err, pageSize)
	}
}

// listAll pages through ListByPrincipal and returns the ids in the order read.
// A store whose page hands back its own cursor fails here rather than hanging.
func (f fixture) listAll(t *testing.T, req limes.ListByPrincipalRequest) []uuid.UUID {
	t.Helper()
	var ids []uuid.UUID
	for c, err := range limes.AllByPrincipal(f.Ctx, f.Store, req) {
		if err != nil {
			t.Fatalf("ListByPrincipal: %v", err)
		}
		ids = append(ids, c.ID)
	}
	return ids
}

func checkListPages(t *testing.T, f fixture) {
	const subject = "agent:paged"
	var want []uuid.UUID
	for range listed {
		want = append(want, f.live(t, subject, uuid.Nil).ID)
	}
	f.live(t, "agent:someone-else", uuid.Nil)
	req := limes.ListByPrincipalRequest{
		TenantID: f.Tenant, PrincipalType: limes.PrincipalTypeAgent, Subject: subject, Limit: pageSize,
	}
	var sizes []int
	var got []uuid.UUID
	for {
		pagePage, err := f.Store.ListByPrincipal(f.Ctx, req)
		page, next := pagePage.Items, pagePage.Next
		if err != nil {
			t.Fatalf("ListByPrincipal: %v", err)
		}
		sizes = append(sizes, len(page))
		for _, c := range page {
			got = append(got, c.ID)
		}
		if next == "" {
			break
		}
		req.Cursor = next
	}
	if !slices.Equal(sizes, []int{pageSize, pageSize, listed - 2*pageSize}) {
		t.Errorf("page sizes = %v", sizes)
	}
	slices.SortFunc(want, compareUUID)
	if !slices.Equal(got, want) {
		t.Errorf("listed %v, want every capability once in ascending id order %v", got, want)
	}
}

func checkListFilters(t *testing.T, f fixture) {
	const subject = "agent:filtered"
	live := f.live(t, subject, uuid.Nil)
	expired := f.insert(t, f.newCapability(subject, uuid.Nil, f.now.Add(-expiredAgo)))
	revoked := f.live(t, subject, uuid.Nil)
	f.revoke(t, revoked.ID, false)
	// The same subject under another principal type is another principal.
	user := f.newCapability(subject, uuid.Nil, f.now.Add(ttl))
	user.Subject.Type, user.Subject.Agent = limes.PrincipalTypeUser, nil
	f.insert(t, user)
	base := limes.ListByPrincipalRequest{TenantID: f.Tenant, PrincipalType: limes.PrincipalTypeAgent, Subject: subject}
	cases := []struct {
		name             string
		expired, revoked bool
		want             []uuid.UUID
	}{
		{"live only", false, false, []uuid.UUID{live.ID}},
		{"with expired", true, false, []uuid.UUID{live.ID, expired.ID}},
		{"with revoked", false, true, []uuid.UUID{live.ID, revoked.ID}},
		{"everything", true, true, []uuid.UUID{live.ID, expired.ID, revoked.ID}},
	}
	for _, tc := range cases {
		req := base
		req.IncludeExpired, req.IncludeRevoked = tc.expired, tc.revoked
		got := f.listAll(t, req)
		slices.SortFunc(got, compareUUID)
		slices.SortFunc(tc.want, compareUUID)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: listed %v, want %v", tc.name, got, tc.want)
		}
	}
}

// checkListExpiryInstant holds a listing to a capability's expiry at its own
// instant: one expiring at the store's now is expired, one a tick later is
// not. It needs the store's clock (Env.Now).
func checkListExpiryInstant(t *testing.T, f fixture) {
	if f.Now == nil {
		t.Skip("Env.Now is not set")
	}
	const subject = "agent:boundary"
	atNow := f.insert(t, f.newCapability(subject, uuid.Nil, f.now))
	justAfter := f.insert(t, f.newCapability(subject, uuid.Nil, f.now.Add(timestampResolution)))
	base := limes.ListByPrincipalRequest{TenantID: f.Tenant, PrincipalType: limes.PrincipalTypeAgent, Subject: subject}
	if got := f.listAll(t, base); !slices.Equal(got, []uuid.UUID{justAfter.ID}) {
		t.Errorf("listed %v, want only %s: %s expires at the store's now", got, justAfter.ID, atNow.ID)
	}
	base.IncludeExpired = true
	got := f.listAll(t, base)
	slices.SortFunc(got, compareUUID)
	want := []uuid.UUID{atNow.ID, justAfter.ID}
	slices.SortFunc(want, compareUUID)
	if !slices.Equal(got, want) {
		t.Errorf("listed with expired %v, want both %v", got, want)
	}
}

// checkInsertTenants holds a store that knows its tenants to minting only
// for a live one: an id it never created is ErrUnknownTenant, one in the
// trash ErrTenantDeleted, and neither is recorded. It needs
// Env.DeleteTenant.
func checkInsertTenants(t *testing.T, f fixture) {
	if f.DeleteTenant == nil {
		t.Skip("Env.DeleteTenant is not set")
	}
	deleted, err := f.newTenant()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.DeleteTenant(deleted); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		tenant uuid.UUID
		want   error
	}{
		{"an unknown tenant", uuid.New(), limes.ErrUnknownTenant},
		{"a deleted tenant", deleted, limes.ErrTenantDeleted},
	} {
		c := f.newCapability("agent:orphan", uuid.Nil, f.now.Add(ttl))
		c.Subject.TenantID = tc.tenant
		if err := f.Store.Insert(f.tenantCtx(tc.tenant), limes.InsertRequest{Capability: c, IssuedBy: issuedBy}); !errors.Is(err, tc.want) {
			t.Errorf("Insert for %s: err = %v, want %v", tc.name, err, tc.want)
		}
		if _, err := f.Store.GetRecord(f.tenantCtx(tc.tenant), c.ID); !errors.Is(err, limes.ErrNotFound) {
			t.Errorf("GetRecord after an Insert for %s: err = %v, want ErrNotFound", tc.name, err)
		}
	}
}

// A listing holds the named tenant's capabilities, never another's of the
// same principal.
func checkListTenants(t *testing.T, f fixture) {
	const subject = "agent:shared"
	other, err := f.newTenant()
	if err != nil {
		t.Fatalf("NewTenant: %v", err)
	}
	mine := f.live(t, subject, uuid.Nil)
	theirs := f.newCapability(subject, uuid.Nil, f.now.Add(ttl))
	theirs.Subject.TenantID = other
	if err := f.Store.Insert(f.tenantCtx(other), limes.InsertRequest{Capability: theirs, IssuedBy: issuedBy}); err != nil {
		t.Fatalf("insert for another tenant: %v", err)
	}
	got := f.listAll(t, limes.ListByPrincipalRequest{TenantID: f.Tenant, PrincipalType: limes.PrincipalTypeAgent, Subject: subject})
	if !slices.Equal(got, []uuid.UUID{mine.ID}) {
		t.Errorf("listed %v, want only this tenant's %v", got, mine.ID)
	}
}

func checkListMalformed(t *testing.T, f fixture) {
	base := limes.ListByPrincipalRequest{TenantID: f.Tenant, PrincipalType: limes.PrincipalTypeAgent, Subject: "agent:x"}
	for name, mutate := range map[string]func(*limes.ListByPrincipalRequest){
		"no tenant":              func(r *limes.ListByPrincipalRequest) { r.TenantID = uuid.Nil },
		"no principal type":      func(r *limes.ListByPrincipalRequest) { r.PrincipalType = limes.PrincipalTypeUnspecified },
		"unknown principal type": func(r *limes.ListByPrincipalRequest) { r.PrincipalType = "robot" },
		"no subject":             func(r *limes.ListByPrincipalRequest) { r.Subject = "" },
		"bad cursor":             func(r *limes.ListByPrincipalRequest) { r.Cursor = "not-a-cursor" },
		"negative limit":         func(r *limes.ListByPrincipalRequest) { r.Limit = -1 },
	} {
		req := base
		mutate(&req)
		if _, err := f.Store.ListByPrincipal(f.Ctx, req); !errors.Is(err, limes.ErrInvalidRequest) {
			t.Errorf("%s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
}

// biscuitCopy is the BiscuitCopy a verifier would read for a copy of capID
// with revocation id id: the suite checks a store against copies no
// verifier read.
func biscuitCopy(capID uuid.UUID, id []byte) limes.BiscuitCopy {
	return link.NewBiscuitCopy(capID, id).(limes.BiscuitCopy)
}

func checkBiscuit(t *testing.T, f fixture) {
	store, ok := f.Store.(limes.BiscuitRevocationStore)
	if !ok {
		t.Skip("the store keeps no Biscuit revocations")
	}
	live := f.live(t, "agent:biscuit", uuid.Nil)
	expired := f.insert(t, f.newCapability("agent:biscuit-old", uuid.Nil, f.now.Add(-expiredAgo)))
	copyID, oldCopyID, otherID := []byte("copy"), []byte("old-copy"), []byte("other")
	revoke := func(capID uuid.UUID, id []byte) error {
		return store.RevokeBiscuit(f.Ctx, limes.RevokeBiscuitRequest{Copy: biscuitCopy(capID, id), Note: "test", Actor: "storetest"})
	}
	isRevoked := func(ids ...[]byte) bool {
		t.Helper()
		r, err := store.IsBiscuitRevoked(f.Ctx, ids)
		if err != nil {
			t.Fatalf("IsBiscuitRevoked: %v", err)
		}
		return r
	}

	if err := revoke(uuid.New(), copyID); !errors.Is(err, limes.ErrNotFound) {
		t.Errorf("a copy of an unknown capability: err = %v, want ErrNotFound", err)
	}
	if err := revoke(live.ID, nil); !errors.Is(err, limes.ErrInvalidRequest) {
		t.Errorf("an empty revocation id: err = %v, want ErrInvalidRequest", err)
	}
	if err := store.RevokeBiscuit(f.Ctx, limes.RevokeBiscuitRequest{}); !errors.Is(err, limes.ErrInvalidRequest) {
		t.Errorf("a request naming no copy: err = %v, want ErrInvalidRequest", err)
	}
	for range 2 { // idempotent
		if err := revoke(live.ID, copyID); err != nil {
			t.Fatalf("RevokeBiscuit: %v", err)
		}
	}
	// A copy attenuated from a revoked one carries the revoked id before its
	// own: every id is looked up, not only the last.
	if !isRevoked(otherID, copyID) || !isRevoked(copyID, otherID) || isRevoked(otherID) {
		t.Error("IsBiscuitRevoked must hold for a token carrying a revoked id, wherever it is, and only then")
	}
	if err := store.RevokeBiscuit(f.Ctx, limes.RevokeBiscuitRequest{
		Copy: biscuitCopy(live.ID, copyID), Note: "again", Actor: "someone-else",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetBiscuitRevocation(f.Ctx, copyID)
	if err != nil || got.CapabilityID != live.ID || !bytes.Equal(got.RevocationID, copyID) ||
		got.Note != "test" || got.Actor != "storetest" || got.RevokedAt.IsZero() {
		t.Errorf("GetBiscuitRevocation = %+v, %v; want the first revoke of %s", got, err, live.ID)
	}
	if _, err := store.GetBiscuitRevocation(f.Ctx, otherID); !errors.Is(err, limes.ErrNotFound) {
		t.Errorf("GetBiscuitRevocation of an id never revoked: err = %v, want ErrNotFound", err)
	}
	if err := revoke(expired.ID, oldCopyID); err != nil {
		t.Fatal(err)
	}
	if got, err := f.Store.Purge(f.Ctx, limes.PurgeRequest{ExpiredFor: purgeGrace}); err != nil || got.Purged != 1 {
		t.Fatalf("Purge = %+v, %v; want the expired capability's record", got, err)
	}
	if isRevoked(oldCopyID) || !isRevoked(copyID) {
		t.Error("Purge must drop the expired capability's copy and keep the live one's")
	}
}

// checkMaxDepth holds IsRevoked to the whole of a chain MaxDelegationDepth
// deep, and Insert to refusing a deeper one, whose root the walk could not
// reach, without writing it.
func checkMaxDepth(t *testing.T, f fixture) {
	root := f.live(t, "agent:root", uuid.Nil)
	leaf := root
	for range limes.MaxDelegationDepth {
		leaf = f.live(t, "agent:link", leaf.ID)
	}
	f.revoke(t, root.ID, false)
	if !f.revoked(t, leaf.ID) {
		t.Errorf("revoking a root did not reach a capability %d delegations below it", limes.MaxDelegationDepth)
	}
	deeper := f.newCapability("agent:deeper", leaf.ID, f.now.Add(ttl))
	if err := f.Store.Insert(f.Ctx, limes.InsertRequest{Capability: deeper, IssuedBy: issuedBy}); !errors.Is(err, limes.ErrDelegationTooDeep) {
		t.Errorf("Insert past MaxDelegationDepth: err = %v, want ErrDelegationTooDeep", err)
	}
	if _, err := f.get(deeper.ID); !errors.Is(err, limes.ErrNotFound) {
		t.Errorf("a record Insert refused as too deep reads back: err = %v, want ErrNotFound", err)
	}
}

// checkMaxDepthCascade holds a cascade to every generation of a chain
// MaxDelegationDepth deep: each link gets an entry of its own, the deepest
// included, not only the first few a shallow walk reaches.
func checkMaxDepthCascade(t *testing.T, f fixture) {
	root := f.live(t, "agent:root", uuid.Nil)
	links := make([]uuid.UUID, 0, limes.MaxDelegationDepth)
	parent := root.ID
	for range limes.MaxDelegationDepth {
		parent = f.live(t, "agent:link", parent).ID
		links = append(links, parent)
	}
	f.revoke(t, root.ID, true)
	for depth, id := range links {
		rec, err := f.Store.GetRecord(f.Ctx, id)
		if err != nil || rec.Revocation == nil || !rec.Revocation.Cascaded {
			t.Fatalf("the link %d delegations below a cascade: %v; want an entry of its own", depth+1, err)
		}
	}
}

// checkPurgeKeepsLineage holds Purge to never removing a capability while a
// child of it stays on record: a chain expiring at one instant, purged a
// record a batch, goes leaf first.
func checkPurgeKeepsLineage(t *testing.T, f fixture) {
	const chain = 4
	expires := f.now.Add(-expiredAgo)
	ids := make([]uuid.UUID, 0, chain)
	parent := uuid.Nil
	for range chain {
		parent = f.insert(t, f.newCapability("agent:link", parent, expires)).ID
		ids = append(ids, parent)
	}
	onRecord := func(id uuid.UUID) bool {
		t.Helper()
		_, err := f.Store.GetRecord(f.Ctx, id)
		if err != nil && !errors.Is(err, limes.ErrNotFound) {
			t.Fatal(err)
		}
		return err == nil
	}
	for range chain {
		if _, err := f.Store.Purge(f.Ctx, limes.PurgeRequest{ExpiredFor: purgeGrace, Limit: 1}); err != nil {
			t.Fatal(err)
		}
		for i := 1; i < chain; i++ {
			if onRecord(ids[i]) && !onRecord(ids[i-1]) {
				t.Fatalf("Purge left link %d on record without its parent", i)
			}
		}
	}
	for i, id := range ids {
		if onRecord(id) {
			t.Errorf("link %d of an expired chain is still on record after %d batches of one", i, chain)
		}
	}
}

// checkInsertKeepsLineage holds Insert to a lineage Purge and IsRevoked can
// rely on: no record without an id, none under a parent not on record, and
// no child expiring after its parent — one with no expiry under a parent
// that has one among them, as Purge would leave it with no parent. None of
// them is recorded.
func checkInsertKeepsLineage(t *testing.T, f fixture) {
	parent := f.live(t, "agent:parent", uuid.Nil)
	noID := f.newCapability("agent:no-id", uuid.Nil, f.now.Add(ttl))
	noID.ID = uuid.Nil
	orphan := f.newCapability("agent:orphan", uuid.New(), f.now.Add(ttl))
	outliving := f.newCapability("agent:outliving", parent.ID, parent.ExpiresAt.Add(time.Second))
	unexpiring := f.newCapability("agent:unexpiring", parent.ID, time.Time{})
	other, err := f.newTenant()
	if err != nil {
		t.Fatalf("NewTenant: %v", err)
	}
	// A lineage stays in one tenant, as Delegate keeps it: a store that scopes
	// rows by tenant could not see the rest of a mixed one, and its IsRevoked
	// walk would miss a revoked ancestor.
	elsewhere := f.newCapability("agent:elsewhere", parent.ID, parent.ExpiresAt)
	elsewhere.Subject.TenantID = other
	// A store that scopes tenants refuses the child in another tenant
	// before it reads the parent: Ctx is not that tenant's.
	elsewhereRefusal := limes.ErrDelegationTooWide
	if f.ScopesTenants {
		elsewhereRefusal = limes.ErrScopeMismatch
	}
	for name, tc := range map[string]struct {
		c    limes.Capability
		want error
	}{
		"no id":                        {noID, limes.ErrInvalidRequest},
		"a parent not on record":       {orphan, limes.ErrInvalidRequest},
		"a child outliving its parent": {outliving, limes.ErrDelegationTooWide},
		"a child with no expiry":       {unexpiring, limes.ErrDelegationTooWide},
		"a child in another tenant":    {elsewhere, elsewhereRefusal},
	} {
		if err := f.Store.Insert(f.Ctx, limes.InsertRequest{Capability: tc.c, IssuedBy: issuedBy}); !errors.Is(err, tc.want) {
			t.Errorf("Insert of %s: err = %v, want %v", name, err, tc.want)
		}
		if tc.c.ID == uuid.Nil {
			continue
		}
		if _, err := f.Store.GetRecord(f.Ctx, tc.c.ID); !errors.Is(err, limes.ErrNotFound) {
			t.Errorf("GetRecord after a refused Insert of %s: err = %v, want ErrNotFound", name, err)
		}
	}
}

// checkPurgeRetention holds Purge to MinPurgeRetention whatever it is asked:
// a capability expired for less is still inside a verifier's leeway, and its
// revocation must stay where IsRevoked finds it.
func checkPurgeRetention(t *testing.T, f fixture) {
	recent := f.insert(t, f.newCapability("agent:recent", uuid.Nil, f.now.Add(-limes.MinPurgeRetention/2)))
	old := f.insert(t, f.newCapability("agent:old", uuid.Nil, f.now.Add(-2*limes.MinPurgeRetention)))
	f.revoke(t, recent.ID, false)
	got, err := f.Store.Purge(f.Ctx, limes.PurgeRequest{})
	if err != nil || got.Purged != 1 {
		t.Fatalf("Purge with the default retention = %+v, %v; want only the capability expired past it", got, err)
	}
	if !f.revoked(t, recent.ID) {
		t.Error("Purge dropped the revocation of a capability expired for less than MinPurgeRetention")
	}
	if _, err := f.get(old.ID); !errors.Is(err, limes.ErrNotFound) {
		t.Errorf("GetRecord of the capability expired past the retention: err = %v, want ErrNotFound", err)
	}
}

// checkRecordIsItsOwn holds a store to an append-only record: neither the
// capability handed to Insert nor one read back shares anything with what is
// stored, so changing it changes no record.
// checkInsertDropsCopies inserts a capability carrying Copies, which only a
// verifier sets on a Biscuit it accepted: the record keeps the capability
// without them, so no read of it passes for a Biscuit copy.
func checkInsertDropsCopies(t *testing.T, f fixture) {
	c := f.newCapability("agent:copies", uuid.Nil, f.now.Add(ttl))
	want := c.Clone()
	c.Copies = []limes.CopyCeiling{{RevocationID: []byte{1}, Requests: &limes.RequestCeiling{Max: 1}}}
	f.insert(t, c)
	got, err := f.get(c.ID)
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if got.Copies != nil {
		t.Errorf("GetRecord read back Copies %+v, want none", got.Copies)
	}
	if diff := diffCapabilities(got, want); diff != "" {
		t.Errorf("Get read back a different capability: %s", diff)
	}
}

func checkRecordIsItsOwn(t *testing.T, f fixture) {
	const subject = "agent:own"
	c := f.newCapability(subject, uuid.Nil, f.now.Add(ttl))
	want := c.Clone()
	f.insert(t, c)
	scribble := func(c *limes.Capability) {
		c.Audience[0] = "scribbled"
		c.Caveats.Ops[0] = "scribbled"
		c.Caveats.ResourcePrefixes[0] = "scribbled/"
		c.Caveats.ResourceURIs[0] = "scribbled/uri"
		c.Caveats.SourceIPCIDR[0] = "0.0.0.0/0"
		c.Subject.Agent.Model = "scribbled"
	}
	check := func(after string) {
		t.Helper()
		got, err := f.get(c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if diff := diffCapabilities(got, want); diff != "" {
			t.Errorf("the record changed after %s: %s", after, diff)
		}
	}
	scribble(&c)
	check("the inserted capability was changed")
	got, err := f.get(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	scribble(&got)
	check("a capability read back was changed")
	page, err := f.Store.ListByPrincipal(f.Ctx, limes.ListByPrincipalRequest{TenantID: f.Tenant, PrincipalType: limes.PrincipalTypeAgent, Subject: subject})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("ListByPrincipal = %+v, %v; want the one capability", page.Items, err)
	}
	scribble(&page.Items[0])
	check("a capability listed was changed")

	// Who issued it, and a Biscuit copy's revocation, are the store's too.
	byAgent := limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: f.Tenant, Subject: "agent:issuer", Agent: &limes.AgentPrincipal{Model: "m"}}
	issued := f.newCapability(subject, uuid.Nil, f.now.Add(ttl))
	if err := f.Store.Insert(f.Ctx, limes.InsertRequest{Capability: issued, IssuedBy: byAgent}); err != nil {
		t.Fatal(err)
	}
	byAgent.Agent.Model = "scribbled"
	r, err := f.Store.GetRecord(f.Ctx, issued.ID)
	if err != nil {
		t.Fatal(err)
	}
	r.IssuedBy.Agent.Model = "scribbled"
	if again, err := f.Store.GetRecord(f.Ctx, issued.ID); err != nil || again.IssuedBy.Agent == nil || again.IssuedBy.Agent.Model != "m" {
		t.Errorf("IssuedBy after changing what was inserted and read = %+v, %v; want the agent model kept", again.IssuedBy.Agent, err)
	}
	store, ok := f.Store.(limes.BiscuitRevocationStore)
	if !ok {
		return
	}
	copyID := []byte("own-copy")
	if err := store.RevokeBiscuit(f.Ctx, limes.RevokeBiscuitRequest{Copy: biscuitCopy(issued.ID, copyID)}); err != nil {
		t.Fatal(err)
	}
	rev, err := store.GetBiscuitRevocation(f.Ctx, copyID)
	if err != nil {
		t.Fatal(err)
	}
	rev.RevocationID[0] = 'X'
	if again, err := store.GetBiscuitRevocation(f.Ctx, copyID); err != nil || !bytes.Equal(again.RevocationID, copyID) {
		t.Errorf("a Biscuit revocation after changing one read = %q, %v; want %q", again.RevocationID, err, copyID)
	}
}

// A call naming Tenant, made under another tenant's context, is refused as
// one — an Insert writing nothing, a delegated child's included — rather
// than answered as if Tenant's rows were not there.
func checkScopeMismatch(t *testing.T, f fixture) {
	if !f.ScopesTenants {
		t.Skip("Env.ScopesTenants not set")
	}
	other, err := f.newTenant()
	if err != nil {
		t.Fatalf("NewTenant: %v", err)
	}
	wrong := f.tenantCtx(other)
	parent := f.live(t, "agent:parent", uuid.Nil)
	for name, c := range map[string]limes.Capability{
		"a root":            f.newCapability("agent:root", uuid.Nil, f.now.Add(ttl)),
		"a delegated child": f.newCapability("agent:child", parent.ID, parent.ExpiresAt),
	} {
		if err := f.Store.Insert(wrong, limes.InsertRequest{Capability: c, IssuedBy: issuedBy}); !errors.Is(err, limes.ErrScopeMismatch) {
			t.Errorf("Insert of %s of Tenant under another tenant's context: err = %v, want ErrScopeMismatch", name, err)
		}
		if _, err := f.get(c.ID); !errors.Is(err, limes.ErrNotFound) {
			t.Errorf("GetRecord of %s after a refused Insert: err = %v, want ErrNotFound", name, err)
		}
	}
	if _, err := f.Store.ListByPrincipal(wrong, limes.ListByPrincipalRequest{
		TenantID: f.Tenant, PrincipalType: parent.Subject.Type, Subject: parent.Subject.Subject,
	}); !errors.Is(err, limes.ErrScopeMismatch) {
		t.Errorf("ListByPrincipal of Tenant under another tenant's context: err = %v, want ErrScopeMismatch", err)
	}
}

// A read-only scope reads Tenant's records and writes none: every call that
// writes is refused as out of scope rather than let through as no rows, and
// the record it would have written, revoked or removed stays as it was.
func checkReadOnlyScope(t *testing.T, f fixture) {
	ro := f.ReadOnlyCtx
	if ro == nil {
		t.Skip("Env.ReadOnlyCtx not set")
	}
	live := f.live(t, "agent:live", uuid.Nil)
	expired := f.insert(t, f.newCapability("agent:expired", uuid.Nil, f.now.Add(-expiredAgo)))
	if _, err := f.Store.GetRecord(ro, live.ID); err != nil {
		t.Errorf("GetRecord under the read-only scope: %v", err)
	}
	fresh := f.newCapability("agent:fresh", uuid.Nil, f.now.Add(ttl))
	copyID := []byte("read-only-copy")
	writes := map[string]func() error{
		"Insert": func() error { return f.Store.Insert(ro, limes.InsertRequest{Capability: fresh, IssuedBy: issuedBy}) },
		"Revoke": func() error { return f.Store.Revoke(ro, limes.RevokeRequest{CapabilityID: live.ID}) },
		"Purge": func() error {
			_, err := f.Store.Purge(ro, limes.PurgeRequest{ExpiredFor: purgeGrace})
			return err
		},
	}
	biscuits, withBiscuits := f.Store.(limes.BiscuitRevocationStore)
	if withBiscuits {
		writes["RevokeBiscuit"] = func() error {
			return biscuits.RevokeBiscuit(ro, limes.RevokeBiscuitRequest{Copy: biscuitCopy(live.ID, copyID)})
		}
	}
	if eraser, ok := f.Store.(limes.TenantRecordEraser); ok {
		writes["EraseTenantRecords"] = func() error {
			_, err := eraser.EraseTenantRecords(ro, limes.EraseTenantRequest{TenantID: f.Tenant})
			return err
		}
	}
	for name, write := range writes {
		if err := write(); !errors.Is(err, limes.ErrScopeMismatch) {
			t.Errorf("%s under the read-only scope: err = %v, want ErrScopeMismatch", name, err)
		}
	}
	if _, err := f.get(fresh.ID); !errors.Is(err, limes.ErrNotFound) {
		t.Errorf("GetRecord after a refused Insert: err = %v, want ErrNotFound", err)
	}
	if f.revoked(t, live.ID) {
		t.Error("a capability is revoked after a refused Revoke or erase")
	}
	if _, err := f.get(expired.ID); err != nil {
		t.Errorf("an expired record after a refused Purge: %v", err)
	}
	if withBiscuits {
		if _, err := biscuits.GetBiscuitRevocation(f.Ctx, copyID); !errors.Is(err, limes.ErrNotFound) {
			t.Errorf("GetBiscuitRevocation after a refused RevokeBiscuit: err = %v, want ErrNotFound", err)
		}
	}
}

// Erasing a tenant's records stops every token of the tenant and removes no
// record a verifier may still need: an id not on record is not revoked, so a
// record gone while its token may verify would let that token through
// again. What Purge would take goes now; the rest stays, revoked, for Purge.
func checkEraseTenant(t *testing.T, f fixture) {
	eraser, ok := f.Store.(limes.TenantRecordEraser)
	if !ok {
		t.Skip("the Store does not implement limes.TenantRecordEraser")
	}
	other, err := f.newTenant()
	if err != nil {
		t.Fatalf("NewTenant: %v", err)
	}
	root := f.live(t, "agent:root", uuid.Nil)
	child := f.live(t, "agent:child", root.ID)
	revoked := f.live(t, "agent:revoked", uuid.Nil)
	f.revoke(t, revoked.ID, false)
	copyID := []byte("erased-tenant-copy")
	biscuits, withBiscuits := f.Store.(limes.BiscuitRevocationStore)
	if withBiscuits {
		if err := biscuits.RevokeBiscuit(f.Ctx, limes.RevokeBiscuitRequest{Copy: biscuitCopy(child.ID, copyID)}); err != nil {
			t.Fatalf("RevokeBiscuit: %v", err)
		}
	}
	expired := f.insert(t, f.newCapability("agent:expired", uuid.Nil, f.now.Add(-expiredAgo)))
	// Expired, but not for MinPurgeRetention: a verifier may still take it.
	recent := f.insert(t, f.newCapability("agent:recent", uuid.Nil, f.now.Add(-limes.MinPurgeRetention/2)))
	theirs := f.newCapability("agent:theirs", uuid.Nil, f.now.Add(ttl))
	theirs.Subject.TenantID = other
	if err := f.Store.Insert(f.tenantCtx(other), limes.InsertRequest{Capability: theirs, IssuedBy: issuedBy}); err != nil {
		t.Fatalf("Insert for another tenant: %v", err)
	}
	req := limes.EraseTenantRequest{TenantID: f.Tenant, Note: "tenant removed", Actor: "storetest"}
	if f.ScopesTenants {
		if _, err := eraser.EraseTenantRecords(f.tenantCtx(other), req); !errors.Is(err, limes.ErrScopeMismatch) {
			t.Errorf("EraseTenantRecords under another tenant's context: err = %v, want ErrScopeMismatch", err)
		}
	}
	if _, err := eraser.EraseTenantRecords(f.Ctx, limes.EraseTenantRequest{}); !errors.Is(err, limes.ErrInvalidRequest) {
		t.Errorf("EraseTenantRecords naming no tenant: err = %v, want ErrInvalidRequest", err)
	}
	got, err := eraser.EraseTenantRecords(f.Ctx, req)
	want := limes.EraseTenantResult{Revoked: 4, Removed: 1, Kept: 4}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("EraseTenantRecords = %+v, %v; want %+v", got, err, want)
	}
	for _, c := range []limes.Capability{root, child, revoked, recent} {
		if !f.revoked(t, c.ID) {
			t.Errorf("%s verifies after its tenant was erased", c.Subject.Subject)
		}
	}
	r, err := f.Store.GetRecord(f.Ctx, root.ID)
	if err != nil || r.Revocation == nil || r.Revocation.Note != req.Note || r.Revocation.Actor != req.Actor {
		t.Errorf("the erased root's record = %+v, %v; want its revocation, noted as the request asks", r, err)
	}
	if withBiscuits {
		if copyRevoked, err := biscuits.IsBiscuitRevoked(f.Ctx, [][]byte{copyID}); err != nil || !copyRevoked {
			t.Errorf("IsBiscuitRevoked of a live child's copy after the erase = %v, %v; want revoked", copyRevoked, err)
		}
	}
	if _, err := f.get(expired.ID); !errors.Is(err, limes.ErrNotFound) {
		t.Errorf("GetRecord of a record Purge would take, after the erase: err = %v, want ErrNotFound", err)
	}
	if r, err := f.Store.GetRecord(f.tenantCtx(other), theirs.ID); err != nil || r.Revocation != nil {
		t.Errorf("another tenant's record after the erase = %+v, %v; want it on record and not revoked", r, err)
	}
	if again, err := eraser.EraseTenantRecords(f.Ctx, req); err != nil || !reflect.DeepEqual(again, limes.EraseTenantResult{Kept: 4}) {
		t.Errorf("EraseTenantRecords again = %+v, %v; want nothing revoked or removed", again, err)
	}
}

// censusWindow is the ExpiringWithin of the census checks: past the soonest
// active record's expiry, short of ttl.
const censusWindow = ttl / 2

// censusFixture records, for Tenant, a capability of every disposition and
// shape a census tells apart, and one active capability for another tenant;
// it returns that tenant. Counted at f.now within censusWindow, Tenant's
// records are:
//
//	agent:   a live root, its child expiring soon, one expiring at the
//	         window's end, one expired and revoked — 4, 3 active, 1 revoked,
//	         1 delegated, 2 expiring soon
//	service: one long expired, one expiring at now — 2, both expired
//	user:    a revoked root and its child, not revoked itself — 2, both
//	         revoked, 1 delegated
func censusFixture(t *testing.T, f fixture) uuid.UUID {
	t.Helper()
	of := func(kind limes.PrincipalType, c limes.Capability) limes.Capability {
		c.Subject.Type = kind
		if kind != limes.PrincipalTypeAgent {
			c.Subject.Agent = nil
		}
		return c
	}
	root := f.live(t, "agent:root", uuid.Nil)
	f.insert(t, f.newCapability("agent:soon", root.ID, f.now.Add(censusWindow/2)))
	f.insert(t, f.newCapability("agent:edge", uuid.Nil, f.now.Add(censusWindow)))
	revokedExpired := f.insert(t, f.newCapability("agent:revoked-expired", uuid.Nil, f.now.Add(-expiredAgo)))
	f.revoke(t, revokedExpired.ID, false)
	f.insert(t, of(limes.PrincipalTypeService, f.newCapability("svc:old", uuid.Nil, f.now.Add(-expiredAgo))))
	f.insert(t, of(limes.PrincipalTypeService, f.newCapability("svc:now", uuid.Nil, f.now)))
	userRoot := f.insert(t, of(limes.PrincipalTypeUser, f.newCapability("user:root", uuid.Nil, f.now.Add(ttl))))
	userChild := f.insert(t, of(limes.PrincipalTypeUser, f.newCapability("user:child", userRoot.ID, f.now.Add(ttl))))
	f.revoke(t, userRoot.ID, false)
	if !f.revoked(t, userChild.ID) {
		t.Fatal("a child of a revoked capability is not revoked")
	}
	other, err := f.newTenant()
	if err != nil {
		t.Fatalf("NewTenant: %v", err)
	}
	theirs := f.newCapability("agent:theirs", uuid.Nil, f.now.Add(ttl))
	theirs.Subject.TenantID = other
	if err := f.Store.Insert(f.tenantCtx(other), limes.InsertRequest{Capability: theirs, IssuedBy: issuedBy}); err != nil {
		t.Fatalf("Insert for another tenant: %v", err)
	}
	return other
}

// allTenantsCtx is the context a census across every tenant runs under.
func (f fixture) allTenantsCtx() context.Context {
	if f.ReadOnlyCtx != nil {
		return f.ReadOnlyCtx
	}
	return f.Ctx
}

// countAll is every row of the census req asks for, a page at a time.
func (f fixture) countAll(t *testing.T, ctx context.Context, req limes.CountRecordsRequest) []limes.RecordCount {
	t.Helper()
	var out []limes.RecordCount
	fetched := map[string]bool{}
	for {
		fetched[req.Cursor] = true
		page, err := f.Census.CountRecords(ctx, req)
		if err != nil {
			t.Fatalf("CountRecords(%+v): %v", req, err)
		}
		out = append(out, page.Items...)
		if page.Next == "" {
			return out
		}
		if fetched[page.Next] {
			t.Fatalf("CountRecords handed back cursor %q again", page.Next)
		}
		req.Cursor = page.Next
	}
}

// Each record counts under one disposition — revoked, through an ancestor
// on record too, over expired, over active — and a census of one tenant
// counts that tenant's only, whole or by principal type; one across every
// tenant counts each, by tenant.
func checkCensus(t *testing.T, f fixture) {
	if f.Census == nil || f.Now == nil {
		t.Skip("Env.Census or Env.Now not set")
	}
	other := censusFixture(t, f)
	mine := limes.CountRecordsRequest{TenantID: f.Tenant, ExpiringWithin: censusWindow}
	whole := limes.RecordCount{Total: 8, Active: 3, Expired: 2, Revoked: 3, Delegated: 2, ExpiringSoon: 2}
	if got := f.countAll(t, f.Ctx, mine); !reflect.DeepEqual(got, []limes.RecordCount{whole}) {
		t.Errorf("census of Tenant = %+v, want %+v", got, whole)
	}
	byType := mine
	byType.ByPrincipalType = true
	want := []limes.RecordCount{
		{PrincipalType: limes.PrincipalTypeAgent, Total: 4, Active: 3, Revoked: 1, Delegated: 1, ExpiringSoon: 2},
		{PrincipalType: limes.PrincipalTypeService, Total: 2, Expired: 2},
		{PrincipalType: limes.PrincipalTypeUser, Total: 2, Revoked: 2, Delegated: 1},
	}
	if got := f.countAll(t, f.Ctx, byType); !reflect.DeepEqual(got, want) {
		t.Errorf("census of Tenant by principal type = %+v, want %+v", got, want)
	}
	noWindow := mine
	noWindow.ExpiringWithin = 0
	if got := f.countAll(t, f.Ctx, noWindow); len(got) != 1 || got[0].ExpiringSoon != 0 {
		t.Errorf("census with no window = %+v, want nothing expiring soon", got)
	}

	all := limes.CountRecordsRequest{AllTenants: true, ExpiringWithin: censusWindow, ByTenant: true}
	mineByTenant, theirs := whole, limes.RecordCount{TenantID: other, Total: 1, Active: 1}
	mineByTenant.TenantID = f.Tenant
	want = []limes.RecordCount{mineByTenant, theirs}
	slices.SortFunc(want, func(a, b limes.RecordCount) int { return bytes.Compare(a.TenantID[:], b.TenantID[:]) })
	if got := f.countAll(t, f.allTenantsCtx(), all); !reflect.DeepEqual(got, want) {
		t.Errorf("census of every tenant by tenant = %+v, want %+v", got, want)
	}
	all.ByTenant = false
	sum := limes.RecordCount{Total: 9, Active: 4, Expired: 2, Revoked: 3, Delegated: 2, ExpiringSoon: 2}
	if got := f.countAll(t, f.allTenantsCtx(), all); !reflect.DeepEqual(got, []limes.RecordCount{sum}) {
		t.Errorf("census of every tenant = %+v, want %+v", got, sum)
	}
}

// A census pages in its order and hands the same rows a page at a time as
// in one; a malformed request, or a census under a scope that does not
// admit what it counts, is refused.
func checkCensusPages(t *testing.T, f fixture) {
	if f.Census == nil || f.Now == nil {
		t.Skip("Env.Census or Env.Now not set")
	}
	other := censusFixture(t, f)
	req := limes.CountRecordsRequest{AllTenants: true, ExpiringWithin: censusWindow, ByTenant: true, ByPrincipalType: true}
	whole := f.countAll(t, f.allTenantsCtx(), req)
	const groups = 4 // Tenant's three principal types and the other tenant's agents
	if len(whole) != groups {
		t.Fatalf("census by tenant and principal type = %+v, want %d rows", whole, groups)
	}
	req.Limit = 1
	if paged := f.countAll(t, f.allTenantsCtx(), req); !reflect.DeepEqual(paged, whole) {
		t.Errorf("census a row at a time = %+v, want %+v", paged, whole)
	}
	for name, bad := range map[string]limes.CountRecordsRequest{
		"no tenant":         {},
		"a tenant and all":  {TenantID: f.Tenant, AllTenants: true},
		"a negative window": {TenantID: f.Tenant, ExpiringWithin: -time.Second},
		"a negative limit":  {TenantID: f.Tenant, Limit: -1},
		"a foreign cursor":  {TenantID: f.Tenant, Cursor: "not-a-cursor"},
	} {
		if _, err := f.Census.CountRecords(f.Ctx, bad); !errors.Is(err, limes.ErrInvalidRequest) {
			t.Errorf("CountRecords with %s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
	if !f.ScopesTenants {
		return
	}
	if _, err := f.Census.CountRecords(f.tenantCtx(other), limes.CountRecordsRequest{TenantID: f.Tenant}); !errors.Is(err, limes.ErrScopeMismatch) {
		t.Errorf("CountRecords of Tenant under another tenant's context: err = %v, want ErrScopeMismatch", err)
	}
	if _, err := f.Census.CountRecords(f.Ctx, limes.CountRecordsRequest{AllTenants: true}); !errors.Is(err, limes.ErrScopeMismatch) {
		t.Errorf("CountRecords of every tenant under one tenant's context: err = %v, want ErrScopeMismatch", err)
	}
}
