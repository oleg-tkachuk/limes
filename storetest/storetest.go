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
// by principal type, a page at a time, and counts no Biscuit copy. When
// Env.Lister is set, a listing of a tenant's records pages in both orders
// with no record skipped or repeated, filters by principal type, subject,
// subject prefix and disposition, gives each record the disposition the
// census counts it under, and shows no other tenant's; when the lister is a
// limes.RecordListingReader too, it reads each record as the listing gives it.
// When Env.BulkRevoker is set, each selector revokes exactly its
// capabilities, a batch at a time, in one tenant or, by client or session,
// in every tenant under Env.AllTenantsCtx alone, and a record's session reads
// back, a child's its parent's; with Env.Census and Env.Lister too, a count
// and a listing by the same selectors name what it revokes. When Env.Exchanges is set, the first claim of a
// subject token wins, racing ones included, and Purge drops it with its
// capability. A check that does not finish within Env.CheckTimeout stops the
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
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/expect"
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
	// Lister lists the store's records. Optional: without it, or without
	// Now, the checks of limes.RecordLister are skipped; without Census
	// too, so are those that hold a listing to the census's counts.
	Lister limes.RecordLister
	// Exchanges records the capability exchanging a subject token issued,
	// over the same records as Store. Optional: without it, the checks of
	// limes.ExchangeStore are skipped.
	Exchanges limes.ExchangeStore
	// BulkRevoker revokes by subject, client or session over the same
	// records as Store, which then records each capability's session.
	// Optional: without it, the checks of limes.BulkRevoker and of sessions
	// on record are skipped.
	BulkRevoker limes.BulkRevoker
	// AllTenantsCtx is a context scoped to writing every tenant's rows, as
	// housekeeping is (pgstore.WithAllTenants), for a revocation across
	// tenants. Optional: without it, Ctx, but for a store that scopes
	// tenants, whose checks of that are then skipped.
	AllTenantsCtx context.Context
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

// newFixture is a fixture on a fresh Env from setup, at the Env's clock, or
// at the current time to the store's resolution when it has none.
func newFixture(t *testing.T, setup func(t *testing.T) Env) fixture {
	env := setup(t)
	now := time.Now().UTC().Truncate(timestampResolution)
	if env.Now != nil {
		now = env.Now()
	}
	return fixture{Env: env, now: now}
}

// newCapability is a capability with every field set, so a store that drops
// fixtureCopyQuota is the quota of copies the fixture's capabilities carry,
// so that a store that drops it fails the round trip.
const fixtureCopyQuota = 5

// one fails the round trip.
func (f fixture) newCapability(subject string, parent uuid.UUID, expires time.Time) limes.Capability {
	return limes.Capability{
		ID:     uuid.New(),
		Issuer: "storetest",
		Subject: limes.Principal{
			Type: limes.PrincipalTypeAgent, TenantID: f.Tenant, Subject: subject,
			Agent: &limes.AgentPrincipal{
				AgentType: "worker", AgentVersion: "1", RunID: uuid.New(), ParentRunID: uuid.New(), Model: "m", MCPClient: "client",
			},
		},
		Audience: []string{"data", "mcp"},
		Caveats: limes.Caveats{
			Ops:                    []limes.Op{limes.OpGet, "tool:search"},
			ResourcePrefixes:       []string{"corpus/"},
			ResourceURIs:           []string{"corpus/index.json"},
			ResourceNamePrefixes:   []string{"corpus/draft-"},
			Requests:               limes.RequestCeiling{Max: 7},
			Budget:                 limes.BudgetCeiling{Max: 3 * limes.NanosPerUnit / 2, UnitCode: "EUR"}, // 1.5 EUR
			Copies:                 limes.CopyQuota{Max: fixtureCopyQuota},
			AllowTaintedRead:       true,
			IdempotencyKeyRequired: true,
			SourceIPCIDR:           []string{"10.0.0.0/8"},
		},
		IssuedAt:        f.now,
		NotBefore:       f.now,
		ExpiresAt:       expires,
		ParentID:        parent,
		ConfirmationJKT: "jkt",
		ClientID:        "storetest-client",
		OnBehalfOf:      new(f.issuer()),
		PriorActors:     []string{"agent:planner"},
	}
}

// issuer is a principal with every field set, so a store that drops one
// from Record.IssuedBy fails the round trip.
func (f fixture) issuer() limes.Principal {
	return limes.Principal{
		Type: limes.PrincipalTypeAgent, TenantID: f.Tenant, Subject: "agent:issuer",
		Agent: &limes.AgentPrincipal{
			AgentType: "planner", AgentVersion: "2", RunID: uuid.New(), ParentRunID: uuid.New(), Model: "issuer-model", MCPClient: "issuer-client",
		},
	}
}

func (f fixture) insert(t *testing.T, c limes.Capability) limes.Capability {
	t.Helper()
	err := f.Store.Insert(f.Ctx, limes.InsertRequest{Capability: c, IssuedBy: issuedBy})
	expect.Must(t, err == nil, "insert %s: %v", c.ID, err)
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
	expect.Must(t, err == nil, "IsRevoked %s: %v", id, err)
	return r
}

func (f fixture) revoke(t *testing.T, id uuid.UUID, cascade bool) limes.RevokeResult {
	t.Helper()
	r, err := f.Store.Revoke(f.Ctx, limes.RevokeRequest{CapabilityID: id, Note: "test", Actor: "storetest", CascadeChildren: cascade})
	expect.Must(t, err == nil, "revoke %s: %v", id, err)
	return r
}

// record is GetRecord of id, failing the test on an error.
func (f fixture) record(t *testing.T, id uuid.UUID) limes.Record {
	t.Helper()
	r, err := f.Store.GetRecord(f.Ctx, id)
	expect.Must(t, err == nil, "GetRecord %s: %v", id, err)
	return r
}

// sameRevocation reports whether a and b are one entry, read back by the
// store's precision either time.
func sameRevocation(a, b limes.Revocation) bool {
	return a.RevokedAt.Equal(b.RevokedAt) && a.Note == b.Note && a.Actor == b.Actor && a.Cascaded == b.Cascaded
}

// Run runs every check as a subtest, each on a fresh Env from setup.
func Run(t *testing.T, setup func(t *testing.T) Env) {
	t.Helper()
	checks := []watchdog.Check[fixture]{
		{Name: "InsertReadsBack", Run: checkRoundTrip},
		{Name: "InsertRefusesADuplicate", Run: checkDuplicate},
		{Name: "ARecordIsNoCallersToChange", Run: checkRecordIsItsOwn},
		{Name: "GetRecordReadsBackIssuanceAndRevocation", Run: checkRecord},
		{Name: "UnknownIDs", Run: checkUnknown},
		{Name: "RevokeIsIdempotentAndReachesDescendants", Run: checkRevoke},
		{Name: "RevocationLineageAnswersAsIsRevoked", Run: checkRevocationLineage},
		{Name: "CascadeRecordsEveryDescendant", Run: checkCascade},
		{Name: "RevocationReachesEveryGeneration", Run: checkDeepChain},
		{Name: "RevocationReachesTheDeepestChainAndInsertRefusesADeeperOne", Run: checkMaxDepth},
		{Name: "CascadeRecordsTheDeepestChain", Run: checkMaxDepthCascade},
		{Name: "PurgeDropsExpiredRecordsAndTheirEntries", Run: checkPurge},
		{Name: "PurgeWorksInBatches", Run: checkPurgeBatches},
		{Name: "PurgeTakesTheDefaultBatchWhenAskedForNone", Run: checkPurgeDefaultLimit},
		{Name: "ARevocationOutlastsTimePassing", Run: checkRevocationOutlastsTime},
		{Name: "ListByPrincipalClampsItsLimit", Run: checkListLimits},
		{Name: "ListByPrincipalEndsOnAFullLastPage", Run: checkListFullLastPage},
		{Name: "PurgeKeepsARevocationAVerifierMayStillNeed", Run: checkPurgeRetention},
		{Name: "PurgeNeverLeavesAChildWithoutItsParent", Run: checkPurgeKeepsLineage},
		{Name: "InsertRefusesABrokenLineage", Run: checkInsertKeepsLineage},
		{Name: "ListByPrincipalPages", Run: checkListPages},
		{Name: "ListByPrincipalFilters", Run: checkListFilters},
		{Name: "ListByPrincipalExpiresAtTheInstant", Run: checkListExpiryInstant},
		{Name: "ListByPrincipalRefusesAMalformedRequest", Run: checkListMalformed},
		{Name: "ListByPrincipalKeepsTenantsApart", Run: checkListTenants},
		{Name: "InsertRefusesATenantItCannotMintFor", Run: checkInsertTenants},
		{Name: "BiscuitRevocation", Run: checkBiscuit},
		{Name: "ACallScopedToAnotherTenantIsRefused", Run: checkScopeMismatch},
		{Name: "AReadOnlyScopeWritesNothing", Run: checkReadOnlyScope},
		{Name: "CensusCountsEachRecordOnce", Run: checkCensus},
		{Name: "CensusPagesAndRefusesAMalformedRequest", Run: checkCensusPages},
		{Name: "CensusCountsNoBiscuitCopyAndNoEmptyGroup", Run: checkCensusEdges},
		{Name: "ListRecordsGivesEachRecordItsCensusDisposition", Run: checkListRecords},
		{Name: "ListRecordsFilters", Run: checkListRecordsFilters},
		{Name: "ListRecordsPagesBothWays", Run: checkListRecordsPages},
		{Name: "ListRecordsRefusesAMalformedRequest", Run: checkListRecordsMalformed},
		{Name: "ListRecordsRefusesACursorOfOtherFilters", Run: checkListRecordsCursorFilters},
		{Name: "GetRecordListingIsTheListingOfTheRecord", Run: checkGetRecordListing},
		{Name: "ListingNamesTheEntryThatRevokedARecord", Run: checkListingStoppedBy},
		{Name: "ListRecordsAcrossTenantsInOneOrder", Run: checkListRecordsAllTenants},
		{Name: "ListRecordsAcrossTenantsRefusesWhatIsNotItsOwn", Run: checkListRecordsAllTenantsRefusals},
		{Name: "EraseTenantRecordsKeepsWhatAVerifierStillNeeds", Run: checkEraseTenant},
		{Name: "ExchangeClaimsConvergeOnOneCapability", Run: checkExchangeClaims},
		{Name: "ExchangeClaimsRefuseWhatIsNotOnRecord", Run: checkExchangeRefusals},
		{Name: "PurgeKeepsAnExchangeWhileItsTokenLives", Run: checkExchangePurge},
		{Name: "ASessionIsRecordedAndAChildTakesItsParents", Run: checkSessionOnRecord},
		{Name: "RevokeMatchingRevokesEverySelectorsCapabilities", Run: checkRevokeMatching},
		{Name: "RevokeMatchingWorksInBatches", Run: checkRevokeMatchingBatches},
		{Name: "RevokeMatchingRefusesAMalformedRequest", Run: checkRevokeMatchingMalformed},
		{Name: "RevokeMatchingAcrossTenantsRevokesTheClientsAndSessionsCapabilities", Run: checkRevokeMatchingAllTenants},
		{Name: "RevokeMatchingAcrossTenantsWorksInBatches", Run: checkRevokeMatchingAllTenantsBatches},
		{Name: "RevokeMatchingAcrossTenantsNeedsEveryTenantsScope", Run: checkRevokeMatchingAllTenantsScope},
		{Name: "CensusAndListingPreviewRevokeMatching", Run: checkRevokeMatchingPreview},
	}
	watchdog.RunChecks(t, func(t *testing.T) fixture { return newFixture(t, setup) }, func(f fixture) time.Duration { return f.CheckTimeout }, checks)
}

func checkRoundTrip(t *testing.T, f fixture) {
	parent := f.live(t, "agent:parent", uuid.Nil())
	want := f.live(t, "agent:child", parent.ID)
	got, err := f.get(want.ID)
	expect.Must(t, err == nil, "GetRecord: %v", err)
	diff := diffCapabilities(got, want)
	expect.Must(t, diff == "", "Get read back a different capability: %s", diff)
	// Each ceiling lifted in turn: a store that drops either Unlimited reads
	// a ceiling of 0 back, and a capability that allows nothing.
	for name, lift := range map[string]func(*limes.Caveats){
		"requests unlimited": func(c *limes.Caveats) { c.Requests = limes.RequestCeiling{Unlimited: true} },
		"budget unlimited":   func(c *limes.Caveats) { c.Budget = limes.BudgetCeiling{Unlimited: true, UnitCode: c.Budget.UnitCode} },
		"copies unlimited":   func(c *limes.Caveats) { c.Copies = limes.CopyQuota{Unlimited: true} },
	} {
		c := f.newCapability("agent:unlimited", uuid.Nil(), f.now.Add(ttl))
		lift(&c.Caveats)
		f.insert(t, c)
		got, err := f.get(c.ID)
		expect.Must(t, err == nil, "%s: GetRecord: %v", name, err)
		diff := diffCapabilities(got, c)
		expect.That(t, diff == "", "%s: Get read back a different capability: %s", name, diff)
	}
}

func checkRecord(t *testing.T, f fixture) {
	parent := f.live(t, "agent:parent", uuid.Nil())
	child := f.live(t, "agent:child", parent.ID)
	checkRecordReadsBack(t, f)
	checkRecordFirstRevocation(t, f, parent.ID)
	rev := f.record(t, child.ID).Revocation
	expect.That(t, rev == nil, "a child stopped through its parent has an entry of its own: %+v", rev)
	checkRecordCascade(t, f)
	for _, id := range []uuid.UUID{uuid.New(), uuid.Nil()} {
		_, err := f.Store.GetRecord(f.Ctx, id)
		expect.That(t, errors.Is(err, limes.ErrNotFound), "GetRecord %s: err = %v, want ErrNotFound", id, err)
	}
}

// checkRecordReadsBack holds GetRecord to the capability inserted, issued by
// whom it was, and no revocation.
func checkRecordReadsBack(t *testing.T, f fixture) {
	issuer := f.issuer()
	issued := f.newCapability("agent:issued", uuid.Nil(), f.now.Add(ttl))
	err := f.Store.Insert(f.Ctx, limes.InsertRequest{Capability: issued, IssuedBy: issuer})
	expect.Must(t, err == nil, "insert %s: %v", issued.ID, err)
	r := f.record(t, issued.ID)
	diff := diffCapabilities(r.Capability, issued)
	expect.That(t, diff == "", "GetRecord read back a different capability: %s", diff)
	expect.That(t, reflect.DeepEqual(r.IssuedBy, issuer) && r.Revocation == nil, "before any revoke: issued by %+v, revocation %+v; want %+v and none", r.IssuedBy, r.Revocation, issuer)
}

// checkRecordFirstRevocation holds a record to its first revoke's entry, as
// both revokes reported it, however often it was revoked since.
func checkRecordFirstRevocation(t *testing.T, f fixture, parent uuid.UUID) {
	first := f.revoke(t, parent, false)
	expect.That(t, !(first.AlreadyRevoked), "first revoke of %s: AlreadyRevoked", parent)
	again, err := f.Store.Revoke(f.Ctx, limes.RevokeRequest{CapabilityID: parent, Note: "again", Actor: "someone-else"})
	expect.Must(t, err == nil, "%v", err)
	rev := f.record(t, parent).Revocation
	if rev == nil || rev.Note != "test" || rev.Actor != "storetest" || rev.Cascaded || rev.RevokedAt.IsZero() {
		t.Errorf("revocation = %+v; want the first revoke's reason and actor, not cascading, with a time", rev)
		return
	}
	expect.That(t, sameRevocation(first.Revocation, *rev), "first revoke reported %+v; on record %+v", first.Revocation, rev)
	expect.That(t, again.AlreadyRevoked && sameRevocation(again.Revocation, *rev), "repeated revoke = %+v; want AlreadyRevoked and the first entry %+v", again, rev)
}

// checkRecordCascade holds a cascading revoke to a cascading entry on the
// root and its child, with every field kept.
func checkRecordCascade(t *testing.T, f fixture) {
	cascadeRoot := f.live(t, "agent:cascade", uuid.Nil())
	cascadeChild := f.live(t, "agent:cascade-child", cascadeRoot.ID)
	f.revoke(t, cascadeRoot.ID, true)
	for _, id := range []uuid.UUID{cascadeRoot.ID, cascadeChild.ID} {
		rev := f.record(t, id).Revocation
		if rev == nil || !rev.Cascaded {
			t.Errorf("%s after a cascading revoke: revocation %+v, want a cascading entry", id, rev)
			continue
		}
		// Every field of a cascading entry is set, so one the store does not
		// keep reads back as unset.
		unset := fields.Unset(*rev)
		expect.That(t, len(unset) == 0, "%s: revocation %+v reads back with %v unset", id, rev, unset)
	}
}

func checkDuplicate(t *testing.T, f fixture) {
	c := f.live(t, "agent:a", uuid.Nil())
	again := c
	again.Subject.Subject = "agent:b"
	err := f.Store.Insert(f.Ctx, limes.InsertRequest{Capability: again, IssuedBy: issuedBy})
	expect.Must(t, errors.Is(err, limes.ErrAlreadyExists), "second insert of %s: err = %v, want ErrAlreadyExists", c.ID, err)
	got, err := f.get(c.ID)
	expect.Must(t, err == nil && got.Subject.Subject == "agent:a", "after a refused insert Get = %+v, %v; want the first record", got, err)
}

func checkUnknown(t *testing.T, f fixture) {
	for _, id := range []uuid.UUID{uuid.New(), uuid.Nil()} {
		_, err := f.get(id)
		expect.That(t, errors.Is(err, limes.ErrNotFound), "Get %s: err = %v, want ErrNotFound", id, err)
		expect.That(t, !(f.revoked(t, id)), "IsRevoked %s = true for an id not on record", id)
	}
	_, err := f.Store.Revoke(f.Ctx, limes.RevokeRequest{CapabilityID: uuid.New()})
	expect.That(t, errors.Is(err, limes.ErrNotFound), "Revoke of an id not on record: err = %v, want ErrNotFound", err)
	// The zero id names nothing: the caller's bug (RevokeRequest.Validate).
	_, err = f.Store.Revoke(f.Ctx, limes.RevokeRequest{})
	expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "Revoke naming no capability: err = %v, want ErrInvalidRequest", err)
}

func checkRevoke(t *testing.T, f fixture) {
	parent := f.live(t, "agent:parent", uuid.Nil())
	child := f.live(t, "agent:child", parent.ID)
	grandchild := f.live(t, "agent:grandchild", child.ID)
	checkRevokeIdempotent(t, f, parent.ID, child.ID)
	checkRevokeStoppedChild(t, f, child.ID)
	checkRevokeExpired(t, f)
	// A cascade over a capability already revoked keeps its entry, and
	// those of its descendants that have one, and writes the others'.
	r := f.revoke(t, parent.ID, true)
	expect.That(t, r.AlreadyRevoked && !r.Revocation.Cascaded, "cascade over a revoked capability = %+v; want AlreadyRevoked and the first, plain entry", r)
	rev := f.record(t, child.ID).Revocation
	expect.That(t, rev != nil && !rev.Cascaded, "child's own entry after the cascade = %+v; want it kept, not cascading", rev)
	rev = f.record(t, grandchild.ID).Revocation
	expect.That(t, rev != nil && rev.Cascaded, "grandchild after the cascade = %+v; want a cascading entry", rev)
}

// checkRevokeIdempotent holds Revoke to writing one entry and reporting it,
// to stopping the parent and its child, and nothing unrelated.
func checkRevokeIdempotent(t *testing.T, f fixture, parent, child uuid.UUID) {
	other := f.live(t, "agent:other", uuid.Nil())
	r := f.revoke(t, parent, false)
	expect.That(t, !r.AlreadyRevoked && r.PriorDisposition == limes.RecordDispositionActive, "first revoke = %+v; want a new entry, active before", r)
	r = f.revoke(t, parent, false) // idempotent
	expect.That(t, r.AlreadyRevoked && r.PriorDisposition == limes.RecordDispositionRevoked, "repeated revoke = %+v; want AlreadyRevoked, revoked before", r)
	expect.That(t, f.revoked(t, parent) && f.revoked(t, child), "revoking a parent must stop it and its child")
	expect.That(t, !(f.revoked(t, other.ID)), "revoking one capability stopped an unrelated one")
}

// checkRevokeStoppedChild holds Revoke to writing an entry for a child
// stopped through its parent, which has none of its own.
func checkRevokeStoppedChild(t *testing.T, f fixture, child uuid.UUID) {
	r := f.revoke(t, child, false)
	if r.AlreadyRevoked || r.Revocation.Cascaded || r.PriorDisposition != limes.RecordDispositionRevoked {
		t.Errorf("first revoke of a child stopped through its parent = %+v; want a new entry, not cascading, revoked before", r)
		return
	}
	rev := f.record(t, child).Revocation
	expect.That(t, rev != nil && sameRevocation(r.Revocation, *rev), "first revoke of a child reported %+v; on record %+v", r.Revocation, rev)
}

// checkRevokeExpired holds Revoke to writing the entry of a capability
// expired before its revoke, and reporting it expired.
func checkRevokeExpired(t *testing.T, f fixture) {
	expired := f.insert(t, f.newCapability("agent:expired", uuid.Nil(), f.now.Add(-expiredAgo)))
	r := f.revoke(t, expired.ID, false)
	expect.That(t, !r.AlreadyRevoked && r.PriorDisposition == limes.RecordDispositionExpired, "revoke of an expired capability = %+v; want a new entry, expired before", r)
	rev := f.record(t, expired.ID).Revocation
	expect.That(t, rev != nil, "revoke of an expired capability wrote no entry")
}

// Cascade writes an entry for each descendant, which ListByPrincipal shows:
// it hides a capability revoked itself, not one revoked through an ancestor.
func checkCascade(t *testing.T, f fixture) {
	const subject = "agent:tree"
	plainRoot := f.live(t, "agent:plain", uuid.Nil())
	plainChild := f.live(t, subject, plainRoot.ID)
	cascadeRoot := f.live(t, "agent:cascade", uuid.Nil())
	cascadeChild := f.live(t, subject, cascadeRoot.ID)
	f.revoke(t, plainRoot.ID, false)
	f.revoke(t, cascadeRoot.ID, true)

	got := f.listAll(t, limes.ListByPrincipalRequest{
		TenantID: f.Tenant, PrincipalType: limes.PrincipalTypeAgent, Subject: subject,
	})
	expect.Must(t, slices.Contains(got, plainChild.ID) && !slices.Contains(got, cascadeChild.ID), "listing hides %v; want only the child revoked by cascade (%s) hidden", got, cascadeChild.ID)
}

// checkDeepChain holds a store to the whole delegation chain, not its first
// link: a store that joins a capability to its parent once, rather than
// walking to the root, passes the one-level checks above and misses a
// grandchild. A cascade must also stay below where it started.
func checkDeepChain(t *testing.T, f fixture) {
	root := f.live(t, "agent:root", uuid.Nil())
	mid := f.live(t, "agent:mid", root.ID)
	leaf := f.live(t, "agent:leaf", mid.ID)
	f.revoke(t, root.ID, false)
	expect.That(t, f.revoked(t, leaf.ID), "revoking a root must stop its grandchild")

	top := f.live(t, "agent:top", uuid.Nil())
	from := f.live(t, "agent:from", top.ID)
	sibling := f.live(t, "agent:sibling", top.ID)
	below := f.live(t, "agent:below", from.ID)
	under := f.live(t, "agent:under", below.ID)
	f.revoke(t, from.ID, true)
	for _, c := range []limes.Capability{from, below, under} {
		rec, err := f.Store.GetRecord(f.Ctx, c.ID)
		expect.That(t, err == nil && rec.Revocation != nil, "GetRecord(%s) = %+v, %v; want a revocation entry written by the cascade", c.Subject.Subject, rec.Revocation, err)
	}
	for _, c := range []limes.Capability{top, sibling} {
		expect.That(t, !(f.revoked(t, c.ID)), "a cascade from %s reached %s, which is not below it", from.Subject.Subject, c.Subject.Subject)
	}
}

func checkPurge(t *testing.T, f fixture) {
	expired := f.insert(t, f.newCapability("agent:old", uuid.Nil(), f.now.Add(-expiredAgo)))
	unrevoked := f.insert(t, f.newCapability("agent:older", uuid.Nil(), f.now.Add(-expiredAgo)))
	live := f.live(t, "agent:new", uuid.Nil())
	// Expired past MinPurgeRetention but within the retention asked for: kept.
	recent := f.insert(t, f.newCapability("agent:recent", uuid.Nil(), f.now.Add(-purgeGrace/2)))
	f.revoke(t, expired.ID, false)
	f.revoke(t, live.ID, false)
	got, err := f.Store.Purge(f.Ctx, limes.PurgeRequest{ExpiredFor: purgeGrace})
	const want = 2 // the two expired records
	expect.Must(t, err == nil && got.Purged == want, "Purge = %+v, %v; want %d records purged", got, err, want)
	for _, id := range []uuid.UUID{expired.ID, unrevoked.ID} {
		_, err := f.get(id)
		expect.That(t, errors.Is(err, limes.ErrNotFound), "an expired record outlived Purge: GetRecord = %v", err)
	}
	expect.That(t, !(f.revoked(t, expired.ID)), "a purged capability is still revoked")
	// An id not on record is not revoked whether or not its record is there,
	// so only a second Insert of the id shows the record gone, not hidden.
	err = f.Store.Insert(f.Ctx, limes.InsertRequest{Capability: unrevoked, IssuedBy: issuedBy})
	expect.That(t, err == nil, "Insert of a purged record's id: %v, want it free again", err)
	_, err = f.get(live.ID)
	expect.That(t, err == nil && f.revoked(t, live.ID), "Purge must keep a live record and its revocation: %v", err)
	_, err = f.get(recent.ID)
	expect.That(t, err == nil, "Purge removed a record expired for less than ExpiredFor: %v", err)
}

// Purge works in batches of Limit, so a housekeeping worker loops while a
// batch comes back full; a negative bound is the request's error.
func checkPurgeBatches(t *testing.T, f fixture) {
	const total, batch = 3, 2
	for range total {
		f.insert(t, f.newCapability("agent:old", uuid.Nil(), f.now.Add(-expiredAgo)))
	}
	first, err := f.Store.Purge(f.Ctx, limes.PurgeRequest{ExpiredFor: purgeGrace, Limit: batch})
	expect.Must(t, err == nil && first.Purged == batch, "first batch = %+v, %v; want %d", first, err, batch)
	second, err := f.Store.Purge(f.Ctx, limes.PurgeRequest{ExpiredFor: purgeGrace, Limit: batch})
	expect.Must(t, err == nil && second.Purged == total-batch, "second batch = %+v, %v; want %d", second, err, total-batch)
	for name, req := range map[string]limes.PurgeRequest{
		"a negative limit":              {Limit: -1},
		"a negative retention":          {ExpiredFor: -time.Second},
		"a retention below the minimum": {ExpiredFor: limes.MinPurgeRetention - time.Second},
	} {
		_, err := f.Store.Purge(f.Ctx, req)
		expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "Purge with %s: err = %v, want ErrInvalidRequest", name, err)
	}
}

// checkPurgeDefaultLimit holds Purge to DefaultHousekeepingLimit when no
// limit is asked for, rather than everything due in one go.
func checkPurgeDefaultLimit(t *testing.T, f fixture) {
	for range limes.DefaultHousekeepingLimit + 1 {
		f.insert(t, f.newCapability("agent:old", uuid.Nil(), f.now.Add(-expiredAgo)))
	}
	got, err := f.Store.Purge(f.Ctx, limes.PurgeRequest{ExpiredFor: purgeGrace})
	expect.That(t, err == nil && got.Purged == limes.DefaultHousekeepingLimit, "Purge with no limit = %+v, %v; want DefaultHousekeepingLimit (%d)", got, err, limes.DefaultHousekeepingLimit)
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
	c := f.insert(t, f.newCapability("agent:long-lived", uuid.Nil(), f.now.Add(lives)))
	f.revoke(t, c.ID, false)
	f.Advance(passes)
	_, err := f.Store.Purge(f.Ctx, limes.PurgeRequest{})
	expect.Must(t, err == nil, "%v", err)
	expect.That(t, f.revoked(t, c.ID), "a live capability revoked %s ago is no longer revoked", passes)
}

// checkListLimits holds ListByPrincipal to its page sizes: DefaultListLimit
// when none is asked for, and never more than MaxListLimit.
func checkListLimits(t *testing.T, f fixture) {
	const subject = "agent:many"
	for range limes.MaxListLimit + 1 {
		f.live(t, subject, uuid.Nil())
	}
	for name, tc := range map[string]struct{ limit, want int }{
		"no limit":      {0, limes.DefaultListLimit},
		"past the most": {2 * limes.MaxListLimit, limes.MaxListLimit},
	} {
		page, err := f.Store.ListByPrincipal(f.Ctx, limes.ListByPrincipalRequest{
			TenantID: f.Tenant, PrincipalType: limes.PrincipalTypeAgent, Subject: subject, Limit: tc.limit,
		})
		expect.That(t, err == nil && len(page.Items) == tc.want && page.Next != "", "%s: %d items, next %q, %v; want %d and a next page", name, len(page.Items), page.Next, err, tc.want)
	}
}

// checkListFullLastPage holds ListByPrincipal to an empty cursor on the last
// page, a page filled exactly included: a cursor there costs the caller a
// request that can only come back empty.
func checkListFullLastPage(t *testing.T, f fixture) {
	const subject = "agent:exact"
	for range pageSize {
		f.live(t, subject, uuid.Nil())
	}
	page, err := f.Store.ListByPrincipal(f.Ctx, limes.ListByPrincipalRequest{
		TenantID: f.Tenant, PrincipalType: limes.PrincipalTypeAgent, Subject: subject, Limit: pageSize,
	})
	expect.That(t, err == nil && len(page.Items) == pageSize && page.Next == "", "an exactly full last page: %d items, next %q, %v; want %d and no next page", len(page.Items), page.Next, err, pageSize)
}

// listAll pages through ListByPrincipal and returns the ids in the order read.
// A store whose page hands back its own cursor fails here rather than hanging.
func (f fixture) listAll(t *testing.T, req limes.ListByPrincipalRequest) []uuid.UUID {
	t.Helper()
	var ids []uuid.UUID
	for c, err := range limes.AllByPrincipal(f.Ctx, f.Store, req) {
		expect.Must(t, err == nil, "ListByPrincipal: %v", err)
		ids = append(ids, c.ID)
	}
	return ids
}

func checkListPages(t *testing.T, f fixture) {
	const subject = "agent:paged"
	var want []uuid.UUID
	for range listed {
		want = append(want, f.live(t, subject, uuid.Nil()).ID)
	}
	f.live(t, "agent:someone-else", uuid.Nil())
	req := limes.ListByPrincipalRequest{
		TenantID: f.Tenant, PrincipalType: limes.PrincipalTypeAgent, Subject: subject, Limit: pageSize,
	}
	var sizes []int
	var got []uuid.UUID
	for {
		pagePage, err := f.Store.ListByPrincipal(f.Ctx, req)
		page, next := pagePage.Items, pagePage.Next
		expect.Must(t, err == nil, "ListByPrincipal: %v", err)
		sizes = append(sizes, len(page))
		for _, c := range page {
			got = append(got, c.ID)
		}
		if next == "" {
			break
		}
		req.Cursor = next
	}
	expect.That(t, slices.Equal(sizes, []int{pageSize, pageSize, listed - 2*pageSize}), "page sizes = %v", sizes)
	slices.SortFunc(want, compareUUID)
	expect.That(t, slices.Equal(got, want), "listed %v, want every capability once in ascending id order %v", got, want)
}

func checkListFilters(t *testing.T, f fixture) {
	const subject = "agent:filtered"
	live := f.live(t, subject, uuid.Nil())
	expired := f.insert(t, f.newCapability(subject, uuid.Nil(), f.now.Add(-expiredAgo)))
	revoked := f.live(t, subject, uuid.Nil())
	f.revoke(t, revoked.ID, false)
	// The same subject under another principal type is another principal.
	user := f.newCapability(subject, uuid.Nil(), f.now.Add(ttl))
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
		expect.That(t, slices.Equal(got, tc.want), "%s: listed %v, want %v", tc.name, got, tc.want)
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
	atNow := f.insert(t, f.newCapability(subject, uuid.Nil(), f.now))
	justAfter := f.insert(t, f.newCapability(subject, uuid.Nil(), f.now.Add(timestampResolution)))
	base := limes.ListByPrincipalRequest{TenantID: f.Tenant, PrincipalType: limes.PrincipalTypeAgent, Subject: subject}
	got := f.listAll(t, base)
	expect.That(t, slices.Equal(got, []uuid.UUID{justAfter.ID}), "listed %v, want only %s: %s expires at the store's now", got, justAfter.ID, atNow.ID)
	base.IncludeExpired = true
	got = f.listAll(t, base)
	slices.SortFunc(got, compareUUID)
	want := []uuid.UUID{atNow.ID, justAfter.ID}
	slices.SortFunc(want, compareUUID)
	expect.That(t, slices.Equal(got, want), "listed with expired %v, want both %v", got, want)
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
	expect.Must(t, err == nil, "%v", err)
	err = f.DeleteTenant(deleted)
	expect.Must(t, err == nil, "%v", err)
	for _, tc := range []struct {
		name   string
		tenant uuid.UUID
		want   error
	}{
		{"an unknown tenant", uuid.New(), limes.ErrUnknownTenant},
		{"a deleted tenant", deleted, limes.ErrTenantDeleted},
	} {
		c := f.newCapability("agent:orphan", uuid.Nil(), f.now.Add(ttl))
		c.Subject.TenantID = tc.tenant
		err := f.Store.Insert(f.tenantCtx(tc.tenant), limes.InsertRequest{Capability: c, IssuedBy: issuedBy})
		expect.That(t, errors.Is(err, tc.want), "Insert for %s: err = %v, want %v", tc.name, err, tc.want)
		_, err = f.Store.GetRecord(f.tenantCtx(tc.tenant), c.ID)
		expect.That(t, errors.Is(err, limes.ErrNotFound), "GetRecord after an Insert for %s: err = %v, want ErrNotFound", tc.name, err)
	}
}

// A listing holds the named tenant's capabilities, never another's of the
// same principal.
func checkListTenants(t *testing.T, f fixture) {
	const subject = "agent:shared"
	other, err := f.newTenant()
	expect.Must(t, err == nil, "NewTenant: %v", err)
	mine := f.live(t, subject, uuid.Nil())
	theirs := f.newCapability(subject, uuid.Nil(), f.now.Add(ttl))
	theirs.Subject.TenantID = other
	err = f.Store.Insert(f.tenantCtx(other), limes.InsertRequest{Capability: theirs, IssuedBy: issuedBy})
	expect.Must(t, err == nil, "insert for another tenant: %v", err)
	got := f.listAll(t, limes.ListByPrincipalRequest{TenantID: f.Tenant, PrincipalType: limes.PrincipalTypeAgent, Subject: subject})
	expect.That(t, slices.Equal(got, []uuid.UUID{mine.ID}), "listed %v, want only this tenant's %v", got, mine.ID)
}

func checkListMalformed(t *testing.T, f fixture) {
	base := limes.ListByPrincipalRequest{TenantID: f.Tenant, PrincipalType: limes.PrincipalTypeAgent, Subject: "agent:x"}
	for name, mutate := range map[string]func(*limes.ListByPrincipalRequest){
		"no tenant":              func(r *limes.ListByPrincipalRequest) { r.TenantID = uuid.Nil() },
		"no principal type":      func(r *limes.ListByPrincipalRequest) { r.PrincipalType = limes.PrincipalTypeUnspecified },
		"unknown principal type": func(r *limes.ListByPrincipalRequest) { r.PrincipalType = "robot" },
		"no subject":             func(r *limes.ListByPrincipalRequest) { r.Subject = "" },
		"bad cursor":             func(r *limes.ListByPrincipalRequest) { r.Cursor = "not-a-cursor" },
		"negative limit":         func(r *limes.ListByPrincipalRequest) { r.Limit = -1 },
	} {
		req := base
		mutate(&req)
		_, err := f.Store.ListByPrincipal(f.Ctx, req)
		expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "%s: err = %v, want ErrInvalidRequest", name, err)
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
	b := biscuitStore{f: f, store: store}
	live := f.live(t, "agent:biscuit", uuid.Nil())
	expired := f.insert(t, f.newCapability("agent:biscuit-old", uuid.Nil(), f.now.Add(-expiredAgo)))
	copyID, otherID := []byte("copy"), []byte("other")
	checkBiscuitRefusals(t, b, live.ID, copyID)
	first := checkBiscuitRevokedOnce(t, b, live.ID, copyID)
	// A copy attenuated from a revoked one carries the revoked id before its
	// own: every id is looked up, not only the last.
	expect.That(t, b.isRevoked(t, otherID, copyID) && b.isRevoked(t, copyID, otherID) && !b.isRevoked(t, otherID), "IsBiscuitRevoked must hold for a token carrying a revoked id, wherever it is, and only then")
	checkBiscuitReadBack(t, b, live.ID, copyID, otherID, first)
	checkBiscuitPurge(t, b, live.ID, expired.ID, copyID)
}

// biscuitStore is the fixture's store as a BiscuitRevocationStore.
type biscuitStore struct {
	f     fixture
	store limes.BiscuitRevocationStore
}

// revoke revokes the copy id of capID as the suite does.
func (b biscuitStore) revoke(capID uuid.UUID, id []byte) error {
	_, err := b.store.RevokeBiscuit(b.f.Ctx, limes.RevokeBiscuitRequest{Copy: biscuitCopy(capID, id), Note: "test", Actor: "storetest"})
	return err
}

// isRevoked reports whether a token carrying ids is revoked.
func (b biscuitStore) isRevoked(t *testing.T, ids ...[]byte) bool {
	t.Helper()
	r, err := b.store.IsBiscuitRevoked(b.f.Ctx, ids)
	expect.Must(t, err == nil, "IsBiscuitRevoked: %v", err)
	return r
}

// checkBiscuitRefusals holds RevokeBiscuit to refusing a copy of an unknown
// capability as ErrNotFound, and an empty id or no copy as
// ErrInvalidRequest.
func checkBiscuitRefusals(t *testing.T, b biscuitStore, live uuid.UUID, copyID []byte) {
	err := b.revoke(uuid.New(), copyID)
	expect.That(t, errors.Is(err, limes.ErrNotFound), "a copy of an unknown capability: err = %v, want ErrNotFound", err)
	err = b.revoke(live, nil)
	expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "an empty revocation id: err = %v, want ErrInvalidRequest", err)
	_, err = b.store.RevokeBiscuit(b.f.Ctx, limes.RevokeBiscuitRequest{})
	expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "a request naming no copy: err = %v, want ErrInvalidRequest", err)
}

// checkBiscuitRevokedOnce holds RevokeBiscuit to writing the entry and
// reporting it the first time, and to writing nothing and reporting the
// first the second; it returns the first entry.
func checkBiscuitRevokedOnce(t *testing.T, b biscuitStore, live uuid.UUID, copyID []byte) limes.BiscuitRevocation {
	t.Helper()
	var first limes.BiscuitRevocation
	for i := range 2 {
		r, err := b.store.RevokeBiscuit(b.f.Ctx, limes.RevokeBiscuitRequest{Copy: biscuitCopy(live, copyID), Note: "test", Actor: "storetest"})
		expect.Must(t, err == nil, "RevokeBiscuit: %v", err)
		expect.That(t, r.AlreadyRevoked == (i > 0) && r.Revocation.CapabilityID == live && bytes.Equal(r.Revocation.RevocationID, copyID) && r.Revocation.Note == "test", "RevokeBiscuit, time %d = %+v; want AlreadyRevoked %v and the first entry", i+1, r, i > 0)
		if i == 0 {
			first = r.Revocation
		} else if !r.Revocation.RevokedAt.Equal(first.RevokedAt) {
			t.Errorf("a repeated RevokeBiscuit reported an entry of %s; the first was of %s", r.Revocation.RevokedAt, first.RevokedAt)
		}
	}
	return first
}

// checkBiscuitReadBack holds GetBiscuitRevocation to the first revoke of a
// copy, however often it was revoked since, and to refusing an id never
// revoked or no copy.
func checkBiscuitReadBack(t *testing.T, b biscuitStore, live uuid.UUID, copyID, otherID []byte, first limes.BiscuitRevocation) {
	_, err := b.store.RevokeBiscuit(b.f.Ctx, limes.RevokeBiscuitRequest{
		Copy: biscuitCopy(live, copyID), Note: "again", Actor: "someone-else",
	})
	expect.Must(t, err == nil, "%v", err)
	got, err := b.store.GetBiscuitRevocation(b.f.Ctx, biscuitCopy(live, copyID))
	expect.That(t, err == nil && got.CapabilityID == live && bytes.Equal(got.RevocationID, copyID) &&
		got.Note == "test" && got.Actor == "storetest" && !got.RevokedAt.IsZero() && got.RevokedAt.Equal(first.RevokedAt), "GetBiscuitRevocation = %+v, %v; want the first revoke of %s, as RevokeBiscuit reported it", got, err, live)
	_, err = b.store.GetBiscuitRevocation(b.f.Ctx, biscuitCopy(live, otherID))
	expect.That(t, errors.Is(err, limes.ErrNotFound), "GetBiscuitRevocation of an id never revoked: err = %v, want ErrNotFound", err)
	_, err = b.store.GetBiscuitRevocation(b.f.Ctx, limes.BiscuitCopy{})
	expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "GetBiscuitRevocation naming no copy: err = %v, want ErrInvalidRequest", err)
}

// checkBiscuitPurge holds Purge to dropping an expired capability's copy
// revocations and keeping a live one's, even of the same id. sharedID is
// one revocation id carried by chains of both capabilities: a Biscuit
// block's signature does not cover the block before it, so a holder of two
// Biscuits can graft one's block onto the other.
func checkBiscuitPurge(t *testing.T, b biscuitStore, live, expired uuid.UUID, copyID []byte) {
	oldCopyID, sharedID := []byte("old-copy"), []byte("grafted-copy")
	err := b.revoke(expired, oldCopyID)
	expect.Must(t, err == nil, "%v", err)
	// The grafted copy is revoked under the expired capability first; the
	// live capability's own revocation of the id is still its own.
	err = b.revoke(expired, sharedID)
	expect.Must(t, err == nil, "%v", err)
	_, err = b.store.RevokeBiscuit(b.f.Ctx, limes.RevokeBiscuitRequest{
		Copy: biscuitCopy(live, sharedID), Note: "live", Actor: "operator",
	})
	expect.Must(t, err == nil, "%v", err)
	got, err := b.store.GetBiscuitRevocation(b.f.Ctx, biscuitCopy(live, sharedID))
	expect.That(t, err == nil && got.CapabilityID == live && got.Note == "live", "GetBiscuitRevocation of the live capability's copy = %+v, %v; want its own revocation, not the expired one's", got, err)
	purged, err := b.f.Store.Purge(b.f.Ctx, limes.PurgeRequest{ExpiredFor: purgeGrace})
	expect.Must(t, err == nil && purged.Purged == 1, "Purge = %+v, %v; want the expired capability's record", purged, err)
	expect.That(t, !b.isRevoked(t, oldCopyID) && b.isRevoked(t, copyID), "Purge must drop the expired capability's copy and keep the live one's")
	expect.That(t, b.isRevoked(t, sharedID), "Purge of one capability must keep another capability's revocation of the same id")
	_, err = b.store.GetBiscuitRevocation(b.f.Ctx, biscuitCopy(expired, sharedID))
	expect.That(t, errors.Is(err, limes.ErrNotFound), "GetBiscuitRevocation of the purged capability's copy: err = %v, want ErrNotFound", err)
}

// checkMaxDepth holds IsRevoked to the whole of a chain MaxDelegationDepth
// deep, and Insert to refusing a deeper one, whose root the walk could not
// reach, without writing it.
func checkMaxDepth(t *testing.T, f fixture) {
	root := f.live(t, "agent:root", uuid.Nil())
	leaf := root
	for range limes.MaxDelegationDepth {
		leaf = f.live(t, "agent:link", leaf.ID)
	}
	f.revoke(t, root.ID, false)
	expect.That(t, f.revoked(t, leaf.ID), "revoking a root did not reach a capability %d delegations below it", limes.MaxDelegationDepth)
	deeper := f.newCapability("agent:deeper", leaf.ID, f.now.Add(ttl))
	err := f.Store.Insert(f.Ctx, limes.InsertRequest{Capability: deeper, IssuedBy: issuedBy})
	expect.That(t, errors.Is(err, limes.ErrDelegationTooDeep), "Insert past MaxDelegationDepth: err = %v, want ErrDelegationTooDeep", err)
	_, err = f.get(deeper.ID)
	expect.That(t, errors.Is(err, limes.ErrNotFound), "a record Insert refused as too deep reads back: err = %v, want ErrNotFound", err)
}

// checkMaxDepthCascade holds a cascade to every generation of a chain
// MaxDelegationDepth deep: each link gets an entry of its own, the deepest
// included, not only the first few a shallow walk reaches.
func checkMaxDepthCascade(t *testing.T, f fixture) {
	root := f.live(t, "agent:root", uuid.Nil())
	links := make([]uuid.UUID, 0, limes.MaxDelegationDepth)
	parent := root.ID
	for range limes.MaxDelegationDepth {
		parent = f.live(t, "agent:link", parent).ID
		links = append(links, parent)
	}
	f.revoke(t, root.ID, true)
	for depth, id := range links {
		rec, err := f.Store.GetRecord(f.Ctx, id)
		expect.Must(t, err == nil && rec.Revocation != nil && rec.Revocation.Cascaded, "the link %d delegations below a cascade: %v; want an entry of its own", depth+1, err)
	}
}

// checkPurgeKeepsLineage holds Purge to never removing a capability while a
// child of it stays on record: a chain expiring at one instant, purged a
// record a batch, goes leaf first.
func checkPurgeKeepsLineage(t *testing.T, f fixture) {
	const chain = 4
	expires := f.now.Add(-expiredAgo)
	ids := make([]uuid.UUID, 0, chain)
	parent := uuid.Nil()
	for range chain {
		parent = f.insert(t, f.newCapability("agent:link", parent, expires)).ID
		ids = append(ids, parent)
	}
	onRecord := func(id uuid.UUID) bool {
		t.Helper()
		_, err := f.Store.GetRecord(f.Ctx, id)
		expect.Must(t, err == nil || errors.Is(err, limes.ErrNotFound), "%v", err)
		return err == nil
	}
	for range chain {
		_, err := f.Store.Purge(f.Ctx, limes.PurgeRequest{ExpiredFor: purgeGrace, Limit: 1})
		expect.Must(t, err == nil, "%v", err)
		for i := 1; i < chain; i++ {
			expect.Must(t, !onRecord(ids[i]) || onRecord(ids[i-1]), "Purge left link %d on record without its parent", i)
		}
	}
	for i, id := range ids {
		expect.That(t, !(onRecord(id)), "link %d of an expired chain is still on record after %d batches of one", i, chain)
	}
}

// checkInsertKeepsLineage holds Insert to a lineage Purge and IsRevoked can
// rely on: no record without an id, none under a parent not on record, and
// no record that never expires, which no verifier accepts and Purge never
// removes, no child expiring after its parent, and no child counting in
// another unit than its parent. None of them is recorded.
func checkInsertKeepsLineage(t *testing.T, f fixture) {
	parent := f.live(t, "agent:parent", uuid.Nil())
	noID := f.newCapability("agent:no-id", uuid.Nil(), f.now.Add(ttl))
	noID.ID = uuid.Nil()
	orphan := f.newCapability("agent:orphan", uuid.New(), f.now.Add(ttl))
	outliving := f.newCapability("agent:outliving", parent.ID, parent.ExpiresAt.Add(time.Second))
	unexpiring := f.newCapability("agent:unexpiring", parent.ID, time.Time{})
	endlessRoot := f.newCapability("agent:endless", uuid.Nil(), time.Time{})
	other, err := f.newTenant()
	expect.Must(t, err == nil, "NewTenant: %v", err)
	// A lineage stays in one tenant, as Delegate keeps it: a store that scopes
	// rows by tenant could not see the rest of a mixed one, and its IsRevoked
	// walk would miss a revoked ancestor.
	elsewhere := f.newCapability("agent:elsewhere", parent.ID, parent.ExpiresAt)
	elsewhere.Subject.TenantID = other
	// A lineage counts in one unit, as Delegate keeps it: metering a child
	// spends from every ancestor.
	otherUnit := f.newCapability("agent:other-unit", parent.ID, parent.ExpiresAt)
	otherUnit.Caveats.Budget.UnitCode = "USD"
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
		"a child with no expiry":       {unexpiring, limes.ErrInvalidRequest},
		"a root with no expiry":        {endlessRoot, limes.ErrInvalidRequest},
		"a child in another tenant":    {elsewhere, elsewhereRefusal},
		"a child in another unit":      {otherUnit, limes.ErrUnitCodeMismatch},
	} {
		err := f.Store.Insert(f.Ctx, limes.InsertRequest{Capability: tc.c, IssuedBy: issuedBy})
		expect.That(t, errors.Is(err, tc.want), "Insert of %s: err = %v, want %v", name, err, tc.want)
		if tc.c.ID == uuid.Nil() {
			continue
		}
		_, err = f.Store.GetRecord(f.Ctx, tc.c.ID)
		expect.That(t, errors.Is(err, limes.ErrNotFound), "GetRecord after a refused Insert of %s: err = %v, want ErrNotFound", name, err)
	}
}

// checkPurgeRetention holds Purge to MinPurgeRetention whatever it is asked:
// a capability expired for less is still inside a verifier's leeway, and its
// revocation must stay where IsRevoked finds it.
func checkPurgeRetention(t *testing.T, f fixture) {
	recent := f.insert(t, f.newCapability("agent:recent", uuid.Nil(), f.now.Add(-limes.MinPurgeRetention/2)))
	old := f.insert(t, f.newCapability("agent:old", uuid.Nil(), f.now.Add(-2*limes.MinPurgeRetention)))
	f.revoke(t, recent.ID, false)
	got, err := f.Store.Purge(f.Ctx, limes.PurgeRequest{})
	expect.Must(t, err == nil && got.Purged == 1, "Purge with the default retention = %+v, %v; want only the capability expired past it", got, err)
	expect.That(t, f.revoked(t, recent.ID), "Purge dropped the revocation of a capability expired for less than MinPurgeRetention")
	_, err = f.get(old.ID)
	expect.That(t, errors.Is(err, limes.ErrNotFound), "GetRecord of the capability expired past the retention: err = %v, want ErrNotFound", err)
}

// checkRecordIsItsOwn holds a store to an append-only record: neither the
// capability handed to Insert nor one read back shares anything with what is
// stored, so changing it changes no record.
func checkRecordIsItsOwn(t *testing.T, f fixture) {
	const subject = "agent:own"
	c := f.newCapability(subject, uuid.Nil(), f.now.Add(ttl))
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
		expect.Must(t, err == nil, "%v", err)
		diff := diffCapabilities(got, want)
		expect.That(t, diff == "", "the record changed after %s: %s", after, diff)
	}
	scribble(&c)
	check("the inserted capability was changed")
	got, err := f.get(c.ID)
	expect.Must(t, err == nil, "%v", err)
	scribble(&got)
	check("a capability read back was changed")
	page, err := f.Store.ListByPrincipal(f.Ctx, limes.ListByPrincipalRequest{TenantID: f.Tenant, PrincipalType: limes.PrincipalTypeAgent, Subject: subject})
	expect.Must(t, err == nil && len(page.Items) == 1, "ListByPrincipal = %+v, %v; want the one capability", page.Items, err)
	scribble(&page.Items[0])
	check("a capability listed was changed")

	// Who issued it, and a Biscuit copy's revocation, are the store's too.
	byAgent := limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: f.Tenant, Subject: "agent:issuer", Agent: &limes.AgentPrincipal{Model: "m"}}
	issued := f.newCapability(subject, uuid.Nil(), f.now.Add(ttl))
	err = f.Store.Insert(f.Ctx, limes.InsertRequest{Capability: issued, IssuedBy: byAgent})
	expect.Must(t, err == nil, "%v", err)
	byAgent.Agent.Model = "scribbled"
	r, err := f.Store.GetRecord(f.Ctx, issued.ID)
	expect.Must(t, err == nil, "%v", err)
	r.IssuedBy.Agent.Model = "scribbled"
	again, err := f.Store.GetRecord(f.Ctx, issued.ID)
	expect.That(t, err == nil && again.IssuedBy.Agent != nil && again.IssuedBy.Agent.Model == "m", "IssuedBy after changing what was inserted and read = %+v, %v; want the agent model kept", again.IssuedBy.Agent, err)
	store, ok := f.Store.(limes.BiscuitRevocationStore)
	if !ok {
		return
	}
	copyID := []byte("own-copy")
	_, err = store.RevokeBiscuit(f.Ctx, limes.RevokeBiscuitRequest{Copy: biscuitCopy(issued.ID, copyID)})
	expect.Must(t, err == nil, "%v", err)
	rev, err := store.GetBiscuitRevocation(f.Ctx, biscuitCopy(issued.ID, copyID))
	expect.Must(t, err == nil, "%v", err)
	rev.RevocationID[0] = 'X'
	reread, err := store.GetBiscuitRevocation(f.Ctx, biscuitCopy(issued.ID, copyID))
	expect.That(t, err == nil && bytes.Equal(reread.RevocationID, copyID), "a Biscuit revocation after changing one read = %q, %v; want %q", reread.RevocationID, err, copyID)
}

// A call naming Tenant, made under another tenant's context, is refused as
// one — an Insert writing nothing, a delegated child's included — rather
// than answered as if Tenant's rows were not there.
func checkScopeMismatch(t *testing.T, f fixture) {
	if !f.ScopesTenants {
		t.Skip("Env.ScopesTenants not set")
	}
	other, err := f.newTenant()
	expect.Must(t, err == nil, "NewTenant: %v", err)
	wrong := f.tenantCtx(other)
	parent := f.live(t, "agent:parent", uuid.Nil())
	for name, c := range map[string]limes.Capability{
		"a root":            f.newCapability("agent:root", uuid.Nil(), f.now.Add(ttl)),
		"a delegated child": f.newCapability("agent:child", parent.ID, parent.ExpiresAt),
	} {
		err := f.Store.Insert(wrong, limes.InsertRequest{Capability: c, IssuedBy: issuedBy})
		expect.That(t, errors.Is(err, limes.ErrScopeMismatch), "Insert of %s of Tenant under another tenant's context: err = %v, want ErrScopeMismatch", name, err)
		_, err = f.get(c.ID)
		expect.That(t, errors.Is(err, limes.ErrNotFound), "GetRecord of %s after a refused Insert: err = %v, want ErrNotFound", name, err)
	}
	_, err = f.Store.ListByPrincipal(wrong, limes.ListByPrincipalRequest{
		TenantID: f.Tenant, PrincipalType: parent.Subject.Type, Subject: parent.Subject.Subject,
	})
	expect.That(t, errors.Is(err, limes.ErrScopeMismatch), "ListByPrincipal of Tenant under another tenant's context: err = %v, want ErrScopeMismatch", err)
	// Revoke names no tenant, so its scope hides the record rather than
	// refusing the call: not visible to the caller is not found.
	_, err = f.Store.Revoke(wrong, limes.RevokeRequest{CapabilityID: parent.ID, Actor: "storetest"})
	expect.That(t, errors.Is(err, limes.ErrNotFound), "Revoke of Tenant's record under another tenant's context: err = %v, want ErrNotFound", err)
	expect.That(t, !f.revoked(t, parent.ID), "a Revoke under another tenant's context revoked the record")
}

// A read-only scope reads Tenant's records and writes none: every call that
// writes is refused as out of scope rather than let through as no rows, and
// the record it would have written, revoked or removed stays as it was.
func checkReadOnlyScope(t *testing.T, f fixture) {
	ro := f.ReadOnlyCtx
	if ro == nil {
		t.Skip("Env.ReadOnlyCtx not set")
	}
	live := f.live(t, "agent:live", uuid.Nil())
	expired := f.insert(t, f.newCapability("agent:expired", uuid.Nil(), f.now.Add(-expiredAgo)))
	_, err := f.Store.GetRecord(ro, live.ID)
	expect.That(t, err == nil, "GetRecord under the read-only scope: %v", err)
	fresh := f.newCapability("agent:fresh", uuid.Nil(), f.now.Add(ttl))
	copyID := []byte("read-only-copy")
	writes := map[string]func() error{
		"Insert": func() error { return f.Store.Insert(ro, limes.InsertRequest{Capability: fresh, IssuedBy: issuedBy}) },
		"Revoke": func() error { _, err := f.Store.Revoke(ro, limes.RevokeRequest{CapabilityID: live.ID}); return err },
		"Purge": func() error {
			_, err := f.Store.Purge(ro, limes.PurgeRequest{ExpiredFor: purgeGrace})
			return err
		},
	}
	biscuits, withBiscuits := f.Store.(limes.BiscuitRevocationStore)
	if withBiscuits {
		writes["RevokeBiscuit"] = func() error {
			_, err := biscuits.RevokeBiscuit(ro, limes.RevokeBiscuitRequest{Copy: biscuitCopy(live.ID, copyID)})
			return err
		}
	}
	if eraser, ok := f.Store.(limes.TenantRecordEraser); ok {
		writes["EraseTenantRecords"] = func() error {
			_, err := eraser.EraseTenantRecords(ro, limes.EraseTenantRequest{TenantID: f.Tenant})
			return err
		}
	}
	for name, write := range writes {
		err := write()
		expect.That(t, errors.Is(err, limes.ErrScopeMismatch), "%s under the read-only scope: err = %v, want ErrScopeMismatch", name, err)
	}
	_, err = f.get(fresh.ID)
	expect.That(t, errors.Is(err, limes.ErrNotFound), "GetRecord after a refused Insert: err = %v, want ErrNotFound", err)
	expect.That(t, !(f.revoked(t, live.ID)), "a capability is revoked after a refused Revoke or erase")
	_, err = f.get(expired.ID)
	expect.That(t, err == nil, "an expired record after a refused Purge: %v", err)
	if withBiscuits {
		_, err := biscuits.GetBiscuitRevocation(f.Ctx, biscuitCopy(live.ID, copyID))
		expect.That(t, errors.Is(err, limes.ErrNotFound), "GetBiscuitRevocation after a refused RevokeBiscuit: err = %v, want ErrNotFound", err)
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
	e := seedErasedTenant(t, f)
	req := limes.EraseTenantRequest{TenantID: f.Tenant, Note: "tenant removed", Actor: "storetest"}
	if f.ScopesTenants {
		_, err := eraser.EraseTenantRecords(f.tenantCtx(e.other), req)
		expect.That(t, errors.Is(err, limes.ErrScopeMismatch), "EraseTenantRecords under another tenant's context: err = %v, want ErrScopeMismatch", err)
	}
	_, err := eraser.EraseTenantRecords(f.Ctx, limes.EraseTenantRequest{})
	expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "EraseTenantRecords naming no tenant: err = %v, want ErrInvalidRequest", err)
	got, err := eraser.EraseTenantRecords(f.Ctx, req)
	want := limes.EraseTenantResult{Revoked: 4, Removed: 1, Kept: 4}
	expect.Must(t, err == nil && reflect.DeepEqual(got, want), "EraseTenantRecords = %+v, %v; want %+v", got, err, want)
	for _, c := range []limes.Capability{e.root, e.child, e.revoked, e.recent} {
		expect.That(t, f.revoked(t, c.ID), "%s verifies after its tenant was erased", c.Subject.Subject)
	}
	checkErasedRecords(t, f, e, req)
	checkErasedBiscuits(t, f, e)
	again, err := eraser.EraseTenantRecords(f.Ctx, req)
	expect.That(t, err == nil && reflect.DeepEqual(again, limes.EraseTenantResult{Kept: 4}), "EraseTenantRecords again = %+v, %v; want nothing revoked or removed", again, err)
}

// erasedTenant is what checkEraseTenant erases, and what it must leave.
type erasedTenant struct {
	other                        uuid.UUID
	root, child, revoked, recent limes.Capability
	expired, theirs              limes.Capability
	copyID, expiredCopyID        []byte
	biscuits                     limes.BiscuitRevocationStore
}

// seedErasedTenant gives the tenant a live root and child, a revoked
// record, an expired one Purge would take and one it would not yet, and
// Biscuit copies of a live child and of the expired record; and another
// tenant a record of its own.
func seedErasedTenant(t *testing.T, f fixture) erasedTenant {
	t.Helper()
	other, err := f.newTenant()
	expect.Must(t, err == nil, "NewTenant: %v", err)
	e := erasedTenant{other: other, copyID: []byte("erased-tenant-copy"), expiredCopyID: []byte("erased-tenant-expired-copy")}
	e.root = f.live(t, "agent:root", uuid.Nil())
	e.child = f.live(t, "agent:child", e.root.ID)
	e.revoked = f.live(t, "agent:revoked", uuid.Nil())
	f.revoke(t, e.revoked.ID, false)
	e.biscuits, _ = f.Store.(limes.BiscuitRevocationStore)
	e.revokeCopy(t, f, e.child.ID, e.copyID, "RevokeBiscuit")
	e.expired = f.insert(t, f.newCapability("agent:expired", uuid.Nil(), f.now.Add(-expiredAgo)))
	e.revokeCopy(t, f, e.expired.ID, e.expiredCopyID, "RevokeBiscuit of the expired record's copy")
	// Expired, but not for MinPurgeRetention: a verifier may still take it.
	e.recent = f.insert(t, f.newCapability("agent:recent", uuid.Nil(), f.now.Add(-limes.MinPurgeRetention/2)))
	e.theirs = f.newCapability("agent:theirs", uuid.Nil(), f.now.Add(ttl))
	e.theirs.Subject.TenantID = other
	err = f.Store.Insert(f.tenantCtx(other), limes.InsertRequest{Capability: e.theirs, IssuedBy: issuedBy})
	expect.Must(t, err == nil, "Insert for another tenant: %v", err)
	return e
}

// revokeCopy revokes the copy id of capID, when the store keeps Biscuit
// revocations.
func (e erasedTenant) revokeCopy(t *testing.T, f fixture, capID uuid.UUID, id []byte, what string) {
	t.Helper()
	if e.biscuits == nil {
		return
	}
	_, err := e.biscuits.RevokeBiscuit(f.Ctx, limes.RevokeBiscuitRequest{Copy: biscuitCopy(capID, id)})
	expect.Must(t, err == nil, "%s: %v", what, err)
}

// checkErasedRecords holds an erase to noting the root's revocation as the
// request asks, removing the record Purge would take, and leaving another
// tenant's record alone.
func checkErasedRecords(t *testing.T, f fixture, e erasedTenant, req limes.EraseTenantRequest) {
	r, err := f.Store.GetRecord(f.Ctx, e.root.ID)
	expect.That(t, err == nil && r.Revocation != nil && r.Revocation.Note == req.Note && r.Revocation.Actor == req.Actor, "the erased root's record = %+v, %v; want its revocation, noted as the request asks", r, err)
	_, err = f.get(e.expired.ID)
	expect.That(t, errors.Is(err, limes.ErrNotFound), "GetRecord of a record Purge would take, after the erase: err = %v, want ErrNotFound", err)
	r, err = f.Store.GetRecord(f.tenantCtx(e.other), e.theirs.ID)
	expect.That(t, err == nil && r.Revocation == nil, "another tenant's record after the erase = %+v, %v; want it on record and not revoked", r, err)
}

// checkErasedBiscuits holds an erase to revoking a live child's copy, and
// removing a removed record's copy with it.
func checkErasedBiscuits(t *testing.T, f fixture, e erasedTenant) {
	if e.biscuits == nil {
		return
	}
	copyRevoked, err := e.biscuits.IsBiscuitRevoked(f.Ctx, [][]byte{e.copyID})
	expect.That(t, err == nil && copyRevoked, "IsBiscuitRevoked of a live child's copy after the erase = %v, %v; want revoked", copyRevoked, err)
	r, err := e.biscuits.GetBiscuitRevocation(f.Ctx, biscuitCopy(e.expired.ID, e.expiredCopyID))
	expect.That(t, errors.Is(err, limes.ErrNotFound), "GetBiscuitRevocation of a removed record's copy = %+v, %v; want ErrNotFound, gone with its record", r, err)
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
	root := f.live(t, "agent:root", uuid.Nil())
	f.insert(t, f.newCapability("agent:soon", root.ID, f.now.Add(censusWindow/2)))
	f.insert(t, f.newCapability("agent:edge", uuid.Nil(), f.now.Add(censusWindow)))
	revokedExpired := f.insert(t, f.newCapability("agent:revoked-expired", uuid.Nil(), f.now.Add(-expiredAgo)))
	f.revoke(t, revokedExpired.ID, false)
	f.insert(t, of(limes.PrincipalTypeService, f.newCapability("svc:old", uuid.Nil(), f.now.Add(-expiredAgo))))
	f.insert(t, of(limes.PrincipalTypeService, f.newCapability("svc:now", uuid.Nil(), f.now)))
	userRoot := f.insert(t, of(limes.PrincipalTypeUser, f.newCapability("user:root", uuid.Nil(), f.now.Add(ttl))))
	userChild := f.insert(t, of(limes.PrincipalTypeUser, f.newCapability("user:child", userRoot.ID, f.now.Add(ttl))))
	f.revoke(t, userRoot.ID, false)
	expect.Must(t, f.revoked(t, userChild.ID), "a child of a revoked capability is not revoked")
	other, err := f.newTenant()
	expect.Must(t, err == nil, "NewTenant: %v", err)
	theirs := f.newCapability("agent:theirs", uuid.Nil(), f.now.Add(ttl))
	theirs.Subject.TenantID = other
	err = f.Store.Insert(f.tenantCtx(other), limes.InsertRequest{Capability: theirs, IssuedBy: issuedBy})
	expect.Must(t, err == nil, "Insert for another tenant: %v", err)
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
		expect.Must(t, err == nil, "CountRecords(%+v): %v", req, err)
		out = append(out, page.Items...)
		if page.Next == "" {
			return out
		}
		expect.Must(t, !(fetched[page.Next]), "CountRecords handed back cursor %q again", page.Next)
		req.Cursor = page.Next
	}
}

// A census of no record is an empty page; a Biscuit copy's revocation is
// not its record's and counts nowhere.
func checkCensusEdges(t *testing.T, f fixture) {
	if f.Census == nil || f.Now == nil {
		t.Skip("Env.Census or Env.Now not set")
	}
	mine := limes.CountRecordsRequest{TenantID: f.Tenant}
	got := f.countAll(t, f.Ctx, mine)
	expect.That(t, len(got) == 0, "census of a tenant with no record = %+v, want an empty page", got)
	live := f.live(t, "agent:copied", uuid.Nil())
	if biscuits, ok := f.Store.(limes.BiscuitRevocationStore); ok {
		_, err := biscuits.RevokeBiscuit(f.Ctx, limes.RevokeBiscuitRequest{Copy: biscuitCopy(live.ID, []byte("census-copy"))})
		expect.Must(t, err == nil, "RevokeBiscuit: %v", err)
	}
	want := []limes.RecordCount{{Total: 1, Active: 1}}
	got = f.countAll(t, f.Ctx, mine)
	expect.That(t, reflect.DeepEqual(got, want), "census = %+v, want %+v", got, want)
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
	got := f.countAll(t, f.Ctx, mine)
	expect.That(t, reflect.DeepEqual(got, []limes.RecordCount{whole}), "census of Tenant = %+v, want %+v", got, whole)
	byType := mine
	byType.ByPrincipalType = true
	want := []limes.RecordCount{
		{PrincipalType: limes.PrincipalTypeAgent, Total: 4, Active: 3, Revoked: 1, Delegated: 1, ExpiringSoon: 2},
		{PrincipalType: limes.PrincipalTypeService, Total: 2, Expired: 2},
		{PrincipalType: limes.PrincipalTypeUser, Total: 2, Revoked: 2, Delegated: 1},
	}
	got = f.countAll(t, f.Ctx, byType)
	expect.That(t, reflect.DeepEqual(got, want), "census of Tenant by principal type = %+v, want %+v", got, want)
	noWindow := mine
	noWindow.ExpiringWithin = 0
	got = f.countAll(t, f.Ctx, noWindow)
	expect.That(t, len(got) == 1 && got[0].ExpiringSoon == 0, "census with no window = %+v, want nothing expiring soon", got)

	all := limes.CountRecordsRequest{AllTenants: true, ExpiringWithin: censusWindow, ByTenant: true}
	mineByTenant, theirs := whole, limes.RecordCount{TenantID: other, Total: 1, Active: 1}
	mineByTenant.TenantID = f.Tenant
	want = []limes.RecordCount{mineByTenant, theirs}
	slices.SortFunc(want, func(a, b limes.RecordCount) int { return bytes.Compare(a.TenantID[:], b.TenantID[:]) })
	got = f.countAll(t, f.allTenantsCtx(), all)
	expect.That(t, reflect.DeepEqual(got, want), "census of every tenant by tenant = %+v, want %+v", got, want)
	all.ByTenant = false
	sum := limes.RecordCount{Total: 9, Active: 4, Expired: 2, Revoked: 3, Delegated: 2, ExpiringSoon: 2}
	got = f.countAll(t, f.allTenantsCtx(), all)
	expect.That(t, reflect.DeepEqual(got, []limes.RecordCount{sum}), "census of every tenant = %+v, want %+v", got, sum)
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
	expect.Must(t, len(whole) == groups, "census by tenant and principal type = %+v, want %d rows", whole, groups)
	req.Limit = 1
	paged := f.countAll(t, f.allTenantsCtx(), req)
	expect.That(t, reflect.DeepEqual(paged, whole), "census a row at a time = %+v, want %+v", paged, whole)
	for name, bad := range map[string]limes.CountRecordsRequest{
		"no tenant":         {},
		"a tenant and all":  {TenantID: f.Tenant, AllTenants: true},
		"a negative window": {TenantID: f.Tenant, ExpiringWithin: -time.Second},
		"a negative limit":  {TenantID: f.Tenant, Limit: -1},
		"a foreign cursor":  {TenantID: f.Tenant, Cursor: "not-a-cursor"},
	} {
		_, err := f.Census.CountRecords(f.Ctx, bad)
		expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "CountRecords with %s: err = %v, want ErrInvalidRequest", name, err)
	}
	if !f.ScopesTenants {
		return
	}
	_, err := f.Census.CountRecords(f.tenantCtx(other), limes.CountRecordsRequest{TenantID: f.Tenant})
	expect.That(t, errors.Is(err, limes.ErrScopeMismatch), "CountRecords of Tenant under another tenant's context: err = %v, want ErrScopeMismatch", err)
	_, err = f.Census.CountRecords(f.Ctx, limes.CountRecordsRequest{AllTenants: true})
	expect.That(t, errors.Is(err, limes.ErrScopeMismatch), "CountRecords of every tenant under one tenant's context: err = %v, want ErrScopeMismatch", err)
}

// checkRevocationLineage holds a store that names lineage
// (limes.RevocationLineageLookup) to its contract, which a RevocationCache's
// ClearRevoked rests on: it answers as IsRevoked at every place of a chain,
// names every ancestor on record, nearest first, while the answer is live —
// one it leaves out keeps a stale answer cached past its ancestor's
// revocation — and an id not on record is ErrNotFound.
func checkRevocationLineage(t *testing.T, f fixture) {
	lineage, ok := f.Store.(limes.RevocationLineageLookup)
	if !ok {
		t.Skip("Env.Store is no limes.RevocationLineageLookup")
	}
	root := f.live(t, "agent:lineage-root", uuid.Nil())
	child := f.live(t, "agent:lineage-child", root.ID)
	grandchild := f.live(t, "agent:lineage-grandchild", child.ID)
	wantLineage := func(what string, id uuid.UUID, ancestors []uuid.UUID) {
		t.Helper()
		a, err := lineage.RevocationLineage(f.Ctx, id)
		expect.Must(t, err == nil, "%s: RevocationLineage: %v", what, err)
		revoked := f.revoked(t, id)
		expect.That(t, a.Revoked == revoked, "%s: RevocationLineage says revoked %v, IsRevoked %v", what, a.Revoked, revoked)
		expect.That(t, a.Revoked || slices.Equal(a.Ancestors, ancestors), "%s: ancestors %v, want %v, nearest first", what, a.Ancestors, ancestors)
	}
	wantLineage("a grandchild", grandchild.ID, []uuid.UUID{child.ID, root.ID})
	wantLineage("a child", child.ID, []uuid.UUID{root.ID})
	wantLineage("a root", root.ID, []uuid.UUID{})
	f.revoke(t, child.ID, false)
	wantLineage("a grandchild under a revoked child", grandchild.ID, nil)
	wantLineage("the revoked child", child.ID, nil)
	wantLineage("a root above it", root.ID, []uuid.UUID{})
	_, err := lineage.RevocationLineage(f.Ctx, uuid.New())
	expect.That(t, errors.Is(err, limes.ErrNotFound), "RevocationLineage of an id not on record: err = %v, want ErrNotFound", err)
}
