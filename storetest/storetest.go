// Package storetest checks a limes.Store — and, when it is one, a
// limes.BiscuitRevocationStore — against the module's contract, the way
// testing/fstest checks an fs.FS. Run it from the store's own tests:
//
//	func TestStoreContract(t *testing.T) {
//		storetest.Run(t, func(t *testing.T) storetest.Env { … })
//	}
//
// The checks cover what every caller relies on: a record reads back as it was
// written, a duplicate or unknown id is refused with the module's sentinel,
// revocation reaches a capability's descendants, purging drops a record
// with its revocation entries once it has been expired long enough and keeps
// a revocation a verifier may still need, a stored record is no caller's to
// change, and listing pages in a fixed order and keeps tenants apart.
package storetest

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/link"
)

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
)

// issuedBy is the principal every capability of these checks is issued by.
var issuedBy = limes.Principal{Type: limes.PrincipalService, Subject: "svc:storetest"}

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
			Type: limes.PrincipalAgent, TenantID: f.Tenant, Subject: subject,
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
			Budget:                 limes.BudgetCeiling{Max: 3 * limes.NanosPerUnit / 2}, // 1.5
			UnitCode:               "EUR",
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
		{"GetRecordReadsBackIssuanceAndRevocation", checkRecord},
		{"UnknownIDs", checkUnknown},
		{"RevokeIsIdempotentAndReachesDescendants", checkRevoke},
		{"CascadeRecordsEveryDescendant", checkCascade},
		{"RevocationReachesEveryGeneration", checkDeepChain},
		{"RevocationReachesTheDeepestChainAndRefusesADeeperOne", checkMaxDepth},
		{"PurgeDropsExpiredRecordsAndTheirEntries", checkPurge},
		{"PurgeWorksInBatches", checkPurgeBatches},
		{"PurgeKeepsARevocationAVerifierMayStillNeed", checkPurgeRetention},
		{"ListByPrincipalPages", checkListPages},
		{"ListByPrincipalFilters", checkListFilters},
		{"ListByPrincipalRefusesAMalformedRequest", checkListMalformed},
		{"ListByPrincipalKeepsTenantsApart", checkListTenants},
		{"BiscuitRevocation", checkBiscuit},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			// Microseconds: the finest a timestamp column keeps.
			c.run(t, fixture{Env: setup(t), now: time.Now().UTC().Truncate(time.Microsecond)})
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
		"budget unlimited":   func(c *limes.Caveats) { c.Budget = limes.BudgetCeiling{Unlimited: true} },
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

	r := record(parent.ID)
	if diff := diffCapabilities(r.Capability, parent); diff != "" {
		t.Errorf("GetRecord read back a different capability: %s", diff)
	}
	if r.IssuedBy != issuedBy || r.Revocation != nil {
		t.Errorf("before any revoke: issued by %+v, revocation %+v; want %+v and none", r.IssuedBy, r.Revocation, issuedBy)
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
		if rev := record(id).Revocation; rev == nil || !rev.Cascaded {
			t.Errorf("%s after a cascading revoke: revocation %+v, want a cascading entry", id, rev)
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
		TenantID: f.Tenant, PrincipalType: limes.PrincipalAgent, Subject: subject,
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
		TenantID: f.Tenant, PrincipalType: limes.PrincipalAgent, Subject: subject, Limit: pageSize,
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
	user.Subject.Type, user.Subject.Agent = limes.PrincipalUser, nil
	f.insert(t, user)
	base := limes.ListByPrincipalRequest{TenantID: f.Tenant, PrincipalType: limes.PrincipalAgent, Subject: subject}
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
	got := f.listAll(t, limes.ListByPrincipalRequest{TenantID: f.Tenant, PrincipalType: limes.PrincipalAgent, Subject: subject})
	if !slices.Equal(got, []uuid.UUID{mine.ID}) {
		t.Errorf("listed %v, want only this tenant's %v", got, mine.ID)
	}
}

func checkListMalformed(t *testing.T, f fixture) {
	base := limes.ListByPrincipalRequest{TenantID: f.Tenant, PrincipalType: limes.PrincipalAgent, Subject: "agent:x"}
	for name, mutate := range map[string]func(*limes.ListByPrincipalRequest){
		"no tenant":              func(r *limes.ListByPrincipalRequest) { r.TenantID = uuid.Nil },
		"no principal type":      func(r *limes.ListByPrincipalRequest) { r.PrincipalType = limes.PrincipalUnspecified },
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
	if !isRevoked(otherID, copyID) || isRevoked(otherID) {
		t.Error("IsBiscuitRevoked must hold for a token carrying a revoked id, and only then")
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
// deep, and to refusing — never clearing — a deeper one, whose root the walk
// cannot reach.
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
	deeper := f.live(t, "agent:deeper", leaf.ID)
	if revoked, err := f.Store.IsRevoked(f.Ctx, deeper.ID); !errors.Is(err, limes.ErrDelegationTooDeep) {
		t.Errorf("IsRevoked past MaxDelegationDepth = %v, %v; want ErrDelegationTooDeep", revoked, err)
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
func checkRecordIsItsOwn(t *testing.T, f fixture) {
	const subject = "agent:own"
	c := f.newCapability(subject, uuid.Nil, f.now.Add(ttl))
	want := c.Clone()
	f.insert(t, c)
	scribble := func(c *limes.Capability) {
		c.Audience[0] = "scribbled"
		c.Caveats.Ops[0] = "scribbled"
		c.Caveats.ResourcePrefixes[0] = "scribbled/"
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
	page, err := f.Store.ListByPrincipal(f.Ctx, limes.ListByPrincipalRequest{TenantID: f.Tenant, PrincipalType: limes.PrincipalAgent, Subject: subject})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("ListByPrincipal = %+v, %v; want the one capability", page.Items, err)
	}
	scribble(&page.Items[0])
	check("a capability listed was changed")
}
