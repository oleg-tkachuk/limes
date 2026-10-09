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
// revocation reaches a capability's descendants, purging drops revocation
// entries and never records, and listing pages in a fixed order.
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
)

// Env is one store under test, fresh for each check.
type Env struct {
	// Ctx is the context every call runs under.
	Ctx context.Context
	// Store is the store under test, with nothing on record.
	Store limes.Store
	// Tenant is a tenant the store can record capabilities for.
	Tenant uuid.UUID
}

const (
	// ttl is how long a live capability of these checks lives; expiredAgo is
	// how long ago an expired one expired, and purgeGrace sits between, so
	// PurgeExpired takes the expired one and leaves the live one.
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
			Agent: &limes.AgentPrincipal{AgentType: "worker", AgentVersion: "1", RunID: uuid.New(), Model: "m"},
		},
		Audience: []string{"data", "mcp"},
		Caveats: limes.Caveats{
			Ops:              []limes.Op{limes.OpGet, "tool:search"},
			ResourcePrefixes: []string{"corpus/"},
			MaxRequests:      7,
			MaxBudgetAmount:  3 * limes.NanosPerUnit / 2, // 1.5
			UnitCode:         "EUR",
			SourceIPCIDR:     []string{"10.0.0.0/8"},
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
	if err := f.Store.Insert(f.Ctx, c, issuedBy); err != nil {
		t.Fatalf("insert %s: %v", c.ID, err)
	}
	return c
}

func (f fixture) live(t *testing.T, subject string, parent uuid.UUID) limes.Capability {
	t.Helper()
	return f.insert(t, f.newCapability(subject, parent, f.now.Add(ttl)))
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
	if err := f.Store.Revoke(f.Ctx, limes.RevokeRequest{ID: id, Reason: "test", Actor: "storetest", CascadeChildren: cascade}); err != nil {
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
		{"GetRecordReadsBackIssuanceAndRevocation", checkRecord},
		{"UnknownIDs", checkUnknown},
		{"RevokeIsIdempotentAndReachesDescendants", checkRevoke},
		{"CascadeRecordsEveryDescendant", checkCascade},
		{"PurgeExpiredDropsEntriesNotRecords", checkPurge},
		{"ListByPrincipalPages", checkListPages},
		{"ListByPrincipalFilters", checkListFilters},
		{"ListByPrincipalRefusesAMalformedRequest", checkListMalformed},
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
	got, err := f.Store.Get(f.Ctx, want.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if diff := diffCapabilities(got, want); diff != "" {
		t.Fatalf("Get read back a different capability: %s", diff)
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
	if err := f.Store.Revoke(f.Ctx, limes.RevokeRequest{ID: parent.ID, Reason: "again", Actor: "someone-else"}); err != nil {
		t.Fatal(err)
	}
	rev := record(parent.ID).Revocation
	if rev == nil || rev.Reason != "test" || rev.Actor != "storetest" || rev.Cascade || rev.RevokedAt.IsZero() {
		t.Errorf("revocation = %+v; want the first revoke's reason and actor, not cascading, with a time", rev)
	}
	if rev := record(child.ID).Revocation; rev != nil {
		t.Errorf("a child stopped through its parent has an entry of its own: %+v", rev)
	}
	f.revoke(t, cascadeRoot.ID, true)
	for _, id := range []uuid.UUID{cascadeRoot.ID, cascadeChild.ID} {
		if rev := record(id).Revocation; rev == nil || !rev.Cascade {
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
	if err := f.Store.Insert(f.Ctx, again, issuedBy); !errors.Is(err, limes.ErrAlreadyExists) {
		t.Fatalf("second insert of %s: err = %v, want ErrAlreadyExists", c.ID, err)
	}
	if got, err := f.Store.Get(f.Ctx, c.ID); err != nil || got.Subject.Subject != "agent:a" {
		t.Fatalf("after a refused insert Get = %+v, %v; want the first record", got, err)
	}
}

func checkUnknown(t *testing.T, f fixture) {
	for _, id := range []uuid.UUID{uuid.New(), uuid.Nil} {
		if _, err := f.Store.Get(f.Ctx, id); !errors.Is(err, limes.ErrNotFound) {
			t.Errorf("Get %s: err = %v, want ErrNotFound", id, err)
		}
		if err := f.Store.Revoke(f.Ctx, limes.RevokeRequest{ID: id}); !errors.Is(err, limes.ErrNotFound) {
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

func checkPurge(t *testing.T, f fixture) {
	expired := f.insert(t, f.newCapability("agent:old", uuid.Nil, f.now.Add(-expiredAgo)))
	live := f.live(t, "agent:new", uuid.Nil)
	f.revoke(t, expired.ID, false)
	f.revoke(t, live.ID, false)
	n, err := f.Store.PurgeExpired(f.Ctx, purgeGrace)
	if err != nil || n != 1 {
		t.Fatalf("PurgeExpired = %d, %v; want the expired capability's one entry", n, err)
	}
	if _, err := f.Store.Get(f.Ctx, expired.ID); err != nil {
		t.Errorf("PurgeExpired dropped a record: Get = %v", err)
	}
	if f.revoked(t, expired.ID) || !f.revoked(t, live.ID) {
		t.Error("PurgeExpired must drop the expired capability's entry and keep the live one's")
	}
}

// listAll pages through ListByPrincipal and returns the ids in the order read.
func (f fixture) listAll(t *testing.T, req limes.ListByPrincipalRequest) []uuid.UUID {
	t.Helper()
	var ids []uuid.UUID
	for {
		page, next, err := f.Store.ListByPrincipal(f.Ctx, req)
		if err != nil {
			t.Fatalf("ListByPrincipal: %v", err)
		}
		for _, c := range page {
			ids = append(ids, c.ID)
		}
		if next == "" {
			return ids
		}
		req.Cursor = next
	}
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
		page, next, err := f.Store.ListByPrincipal(f.Ctx, req)
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
	slices.SortFunc(want, func(a, b uuid.UUID) int { return compareUUID(a, b) })
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

func checkListMalformed(t *testing.T, f fixture) {
	base := limes.ListByPrincipalRequest{TenantID: f.Tenant, PrincipalType: limes.PrincipalAgent, Subject: "agent:x"}
	for name, mutate := range map[string]func(*limes.ListByPrincipalRequest){
		"no tenant":         func(r *limes.ListByPrincipalRequest) { r.TenantID = uuid.Nil },
		"no principal type": func(r *limes.ListByPrincipalRequest) { r.PrincipalType = "" },
		"no subject":        func(r *limes.ListByPrincipalRequest) { r.Subject = "" },
		"bad cursor":        func(r *limes.ListByPrincipalRequest) { r.Cursor = "not-a-cursor" },
	} {
		req := base
		mutate(&req)
		if _, _, err := f.Store.ListByPrincipal(f.Ctx, req); !errors.Is(err, limes.ErrInvalidRequest) {
			t.Errorf("%s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
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
		return store.RevokeBiscuit(f.Ctx, limes.RevokeBiscuitRequest{CapabilityID: capID, RevocationID: id, Reason: "test", Actor: "storetest"})
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
	for range 2 { // idempotent
		if err := revoke(live.ID, copyID); err != nil {
			t.Fatalf("RevokeBiscuit: %v", err)
		}
	}
	if !isRevoked(otherID, copyID) || isRevoked(otherID) {
		t.Error("IsBiscuitRevoked must hold for a token carrying a revoked id, and only then")
	}
	if err := store.RevokeBiscuit(f.Ctx, limes.RevokeBiscuitRequest{
		CapabilityID: live.ID, RevocationID: copyID, Reason: "again", Actor: "someone-else",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetBiscuitRevocation(f.Ctx, copyID)
	if err != nil || got.CapabilityID != live.ID || !bytes.Equal(got.RevocationID, copyID) ||
		got.Reason != "test" || got.Actor != "storetest" || got.RevokedAt.IsZero() {
		t.Errorf("GetBiscuitRevocation = %+v, %v; want the first revoke of %s", got, err, live.ID)
	}
	if _, err := store.GetBiscuitRevocation(f.Ctx, otherID); !errors.Is(err, limes.ErrNotFound) {
		t.Errorf("GetBiscuitRevocation of an id never revoked: err = %v, want ErrNotFound", err)
	}
	if err := revoke(expired.ID, oldCopyID); err != nil {
		t.Fatal(err)
	}
	if n, err := f.Store.PurgeExpired(f.Ctx, purgeGrace); err != nil || n != 1 {
		t.Fatalf("PurgeExpired = %d, %v; want the expired capability's one copy", n, err)
	}
	if isRevoked(oldCopyID) || !isRevoked(copyID) {
		t.Error("PurgeExpired must drop the expired capability's copy and keep the live one's")
	}
}
