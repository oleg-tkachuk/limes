package storetest

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"slices"
	"testing"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/expect"
)

// The selectors the bulk revocation checks match on.
const (
	bulkSession      = "idp-session-1"
	otherSession     = "idp-session-2"
	bulkClient       = "bulk-client"
	bulkOnBehalfOf   = "user:on-behalf"
	revokeAllBatch   = 2
	revokeAllMatches = 5
)

// requireBulkRevoker skips a check of limes.BulkRevoker without one.
func (f fixture) requireBulkRevoker(t *testing.T) {
	t.Helper()
	if f.BulkRevoker == nil {
		t.Skip("Env.BulkRevoker not set")
	}
}

// insertWith records c, changed by change, under session.
func (f fixture) insertWith(t *testing.T, subject string, session string, change func(*limes.Capability)) limes.Capability {
	t.Helper()
	c := f.newCapability(subject, uuid.Nil(), f.now.Add(ttl))
	if change != nil {
		change(&c)
	}
	err := f.Store.Insert(f.Ctx, limes.InsertRequest{Capability: c, IssuedBy: issuedBy, Session: session})
	expect.Must(t, err == nil, "insert %s: %v", c.ID, err)
	return c
}

// ownEntry reports whether id has a revocation entry of its own.
func (f fixture) ownEntry(t *testing.T, id uuid.UUID) bool {
	t.Helper()
	return f.record(t, id).Revocation != nil
}

// The session Insert is given reads back with the record; a child takes its
// parent's, and a child naming one of its own is refused.
func checkSessionOnRecord(t *testing.T, f fixture) {
	f.requireBulkRevoker(t)
	parent := f.insertWith(t, "agent:parent", bulkSession, nil)
	expect.That(t, f.record(t, parent.ID).Session == bulkSession, "the session read back = %q, want %q", f.record(t, parent.ID).Session, bulkSession)
	child := f.newCapability("agent:child", parent.ID, f.now.Add(ttl))
	expect.Must(t, f.Store.Insert(f.Ctx, limes.InsertRequest{Capability: child, IssuedBy: issuedBy}) == nil, "insert the child")
	expect.That(t, f.record(t, child.ID).Session == bulkSession, "the child's session = %q, want its parent's %q", f.record(t, child.ID).Session, bulkSession)
	stray := f.newCapability("agent:stray", parent.ID, f.now.Add(ttl))
	err := f.Store.Insert(f.Ctx, limes.InsertRequest{Capability: stray, IssuedBy: issuedBy, Session: otherSession})
	expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "a child naming a session of its own: %v, want ErrInvalidRequest", err)
	none := f.live(t, "agent:none", uuid.Nil())
	expect.That(t, f.record(t, none.ID).Session == "", "a capability issued under no session reads back %q", f.record(t, none.ID).Session)
}

// Each selector revokes exactly its capabilities, given together they must
// all match, an entry already there stands, and the descendants of what it
// revokes stop through the lineage.
func checkRevokeMatching(t *testing.T, f fixture) {
	f.requireBulkRevoker(t)
	bySession := f.insertWith(t, "agent:a", bulkSession, nil)
	child := f.insert(t, f.newCapability("agent:child", bySession.ID, f.now.Add(ttl)))
	byClient := f.insertWith(t, "agent:b", "", func(c *limes.Capability) { c.ClientID = bulkClient })
	byOnBehalf := f.insertWith(t, "agent:c", "", func(c *limes.Capability) {
		c.OnBehalfOf = &limes.Principal{Type: limes.PrincipalTypeUser, TenantID: f.Tenant, Subject: bulkOnBehalfOf}
	})
	bySubject := f.insertWith(t, "user:leaving", "", nil)
	untouched := f.insertWith(t, "agent:d", otherSession, nil)
	already := f.insertWith(t, "user:leaving", "", nil)
	first := f.revoke(t, already.ID, false)
	for name, tc := range map[string]struct {
		req  limes.RevokeMatchingRequest
		want []uuid.UUID
	}{
		"a session":      {limes.RevokeMatchingRequest{Session: bulkSession}, []uuid.UUID{bySession.ID, child.ID}},
		"a client":       {limes.RevokeMatchingRequest{ClientID: bulkClient}, []uuid.UUID{byClient.ID}},
		"an on-behalf":   {limes.RevokeMatchingRequest{OnBehalfOf: bulkOnBehalfOf}, []uuid.UUID{byOnBehalf.ID}},
		"a subject":      {limes.RevokeMatchingRequest{Subject: "user:leaving"}, []uuid.UUID{bySubject.ID}},
		"all must match": {limes.RevokeMatchingRequest{Subject: "agent:d", Session: bulkSession}, nil},
	} {
		tc.req.TenantID, tc.req.Note, tc.req.Actor = f.Tenant, "storetest", "storetest"
		got, err := f.BulkRevoker.RevokeMatching(f.Ctx, tc.req)
		expect.Must(t, err == nil, "%s: RevokeMatching: %v", name, err)
		var tenants []uuid.UUID
		if len(tc.want) > 0 {
			tenants = []uuid.UUID{f.Tenant}
		}
		expect.That(t, got.Revoked == int64(len(tc.want)) && !got.More && slices.Equal(got.Tenants, tenants),
			"%s: RevokeMatching = %+v, want %d revoked in %v and no more", name, got, len(tc.want), tenants)
		for _, id := range tc.want {
			expect.That(t, f.ownEntry(t, id), "%s: %s has no entry of its own", name, id)
		}
	}
	expect.That(t, !f.revoked(t, untouched.ID), "a capability no selector matched is revoked")
	expect.That(t, f.revoked(t, child.ID), "the child of a revoked match still verifies")
	kept := f.record(t, already.ID).Revocation
	expect.That(t, kept != nil && sameRevocation(*kept, first.Revocation), "an entry already there was replaced: %+v", kept)
	again, err := f.BulkRevoker.RevokeMatching(f.Ctx, limes.RevokeMatchingRequest{TenantID: f.Tenant, Subject: "user:leaving"})
	expect.That(t, err == nil && again.Revoked == 0 && !again.More, "a second call = %+v, %v; want nothing left", again, err)
	other, err := f.newTenant()
	expect.Must(t, err == nil, "NewTenant: %v", err)
	theirs, err := f.BulkRevoker.RevokeMatching(f.tenantCtx(other), limes.RevokeMatchingRequest{TenantID: other, Session: otherSession})
	expect.That(t, err == nil && theirs.Revoked == 0, "another tenant's call = %+v, %v; want nothing of this tenant's", theirs, err)
	expect.That(t, !f.revoked(t, untouched.ID), "another tenant's call revoked this tenant's capability")
}

// A batch revokes at most Limit and says when more may match; the loop
// ends with everything revoked.
func checkRevokeMatchingBatches(t *testing.T, f fixture) {
	f.requireBulkRevoker(t)
	for range revokeAllMatches {
		f.insertWith(t, "agent:many", bulkSession, nil)
	}
	req := limes.RevokeMatchingRequest{TenantID: f.Tenant, Session: bulkSession, Limit: revokeAllBatch}
	var total int64
	for calls := 0; ; calls++ {
		expect.Must(t, calls <= revokeAllMatches, "RevokeMatching never reported the last batch")
		got, err := f.BulkRevoker.RevokeMatching(f.Ctx, req)
		expect.Must(t, err == nil, "RevokeMatching: %v", err)
		expect.That(t, got.Revoked <= revokeAllBatch, "a batch revoked %d, past its limit %d", got.Revoked, revokeAllBatch)
		total += got.Revoked
		if !got.More {
			break
		}
	}
	expect.That(t, total == revokeAllMatches, "the batches revoked %d, want %d", total, revokeAllMatches)
}

func checkRevokeMatchingMalformed(t *testing.T, f fixture) {
	f.requireBulkRevoker(t)
	for name, req := range map[string]limes.RevokeMatchingRequest{
		"no tenant":                    {Subject: "user"},
		"no selector":                  {TenantID: f.Tenant},
		"a negative limit":             {TenantID: f.Tenant, Subject: "user", Limit: -1},
		"a tenant beside every tenant": {TenantID: f.Tenant, AllTenants: true, ClientID: "client"},
		"every tenant by subject":      {AllTenants: true, ClientID: "client", Subject: "user"},
		"every tenant by on-behalf-of": {AllTenants: true, OnBehalfOf: "user"},
	} {
		_, err := f.BulkRevoker.RevokeMatching(f.Ctx, req)
		expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "%s: %v, want ErrInvalidRequest", name, err)
	}
	if !f.ScopesTenants {
		return
	}
	other, err := f.newTenant()
	expect.Must(t, err == nil, "NewTenant: %v", err)
	_, err = f.BulkRevoker.RevokeMatching(f.Ctx, limes.RevokeMatchingRequest{TenantID: other, Subject: "user"})
	expect.That(t, errors.Is(err, limes.ErrScopeMismatch), "a call scoped to another tenant: %v, want ErrScopeMismatch", err)
}

// writeAllTenantsCtx is the context a revocation across every tenant runs
// under, skipping the check where the store scopes tenants and the Env
// names none.
func (f fixture) writeAllTenantsCtx(t *testing.T) context.Context {
	t.Helper()
	switch {
	case f.AllTenantsCtx != nil:
		return f.AllTenantsCtx
	case f.ScopesTenants:
		t.Skip("Env.AllTenantsCtx not set")
	}
	return f.Ctx
}

// insertIn records a root capability of tenant under session, changed by
// change.
func (f fixture) insertIn(t *testing.T, tenant uuid.UUID, session string, change func(*limes.Capability)) limes.Capability {
	t.Helper()
	c := f.newCapability("agent:anywhere", uuid.Nil(), f.now.Add(ttl))
	c.Subject.TenantID = tenant
	if change != nil {
		change(&c)
	}
	err := f.Store.Insert(f.tenantCtx(tenant), limes.InsertRequest{Capability: c, IssuedBy: issuedBy, Session: session})
	expect.Must(t, err == nil, "insert %s for tenant %s: %v", c.ID, tenant, err)
	return c
}

// sortedTenants is tenants in ascending order, as RevokeMatchingResult
// gives them.
func sortedTenants(tenants ...uuid.UUID) []uuid.UUID {
	out := slices.Clone(tenants)
	slices.SortFunc(out, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return out
}

// A client's capabilities in two tenants are revoked by one call across
// tenants, and so are a session's; another client's and another session's
// stay live in both. Each selector is a fresh name, as a store's other
// tenants may hold capabilities under the fixed ones.
func checkRevokeMatchingAllTenants(t *testing.T, f fixture) {
	f.requireBulkRevoker(t)
	all := f.writeAllTenantsCtx(t)
	other, err := f.newTenant()
	expect.Must(t, err == nil, "NewTenant: %v", err)
	client, otherClient := "client-"+uuid.New().String(), "client-"+uuid.New().String()
	session, otherSession := "sid-"+uuid.New().String(), "sid-"+uuid.New().String()
	byClient := func(id string) func(*limes.Capability) { return func(c *limes.Capability) { c.ClientID = id } }
	for name, tc := range map[string]struct {
		req        limes.RevokeMatchingRequest
		mine, kept func(tenant uuid.UUID) limes.Capability
	}{
		"a client": {
			limes.RevokeMatchingRequest{AllTenants: true, ClientID: client},
			func(tenant uuid.UUID) limes.Capability { return f.insertIn(t, tenant, "", byClient(client)) },
			func(tenant uuid.UUID) limes.Capability { return f.insertIn(t, tenant, "", byClient(otherClient)) },
		},
		"a session": {
			limes.RevokeMatchingRequest{AllTenants: true, Session: session},
			func(tenant uuid.UUID) limes.Capability { return f.insertIn(t, tenant, session, nil) },
			func(tenant uuid.UUID) limes.Capability { return f.insertIn(t, tenant, otherSession, nil) },
		},
	} {
		ours, theirs := tc.mine(f.Tenant), tc.mine(other)
		keptOurs, keptTheirs := tc.kept(f.Tenant), tc.kept(other)
		tc.req.Note, tc.req.Actor = "storetest", "storetest"
		got, err := f.BulkRevoker.RevokeMatching(all, tc.req)
		expect.Must(t, err == nil, "%s: RevokeMatching across tenants: %v", name, err)
		want := sortedTenants(f.Tenant, other)
		expect.That(t, got.Revoked == 2 && !got.More && slices.Equal(got.Tenants, want),
			"%s: RevokeMatching across tenants = %+v, want 2 revoked in %v and no more", name, got, want)
		for _, id := range []uuid.UUID{ours.ID, theirs.ID} {
			expect.That(t, f.revoked(t, id), "%s: %s still verifies", name, id)
		}
		for _, id := range []uuid.UUID{keptOurs.ID, keptTheirs.ID} {
			expect.That(t, !f.revoked(t, id), "%s: %s, another's, is revoked", name, id)
		}
		again, err := f.BulkRevoker.RevokeMatching(all, tc.req)
		expect.That(t, err == nil && again.Revoked == 0 && len(again.Tenants) == 0 && !again.More,
			"%s: a second call = %+v, %v; want nothing left", name, again, err)
	}
}

// Across tenants too, a batch revokes at most Limit and says when more may
// match, and the loop ends with every match in every tenant revoked.
func checkRevokeMatchingAllTenantsBatches(t *testing.T, f fixture) {
	f.requireBulkRevoker(t)
	all := f.writeAllTenantsCtx(t)
	other, err := f.newTenant()
	expect.Must(t, err == nil, "NewTenant: %v", err)
	client := "client-" + uuid.New().String()
	var ids []uuid.UUID
	for i := range revokeAllMatches {
		tenant := f.Tenant
		if i%2 == 1 {
			tenant = other
		}
		ids = append(ids, f.insertIn(t, tenant, "", func(c *limes.Capability) { c.ClientID = client }).ID)
	}
	req := limes.RevokeMatchingRequest{AllTenants: true, ClientID: client, Limit: revokeAllBatch}
	var total int64
	for calls := 0; ; calls++ {
		expect.Must(t, calls <= revokeAllMatches, "RevokeMatching across tenants never reported the last batch")
		got, err := f.BulkRevoker.RevokeMatching(all, req)
		expect.Must(t, err == nil, "RevokeMatching across tenants: %v", err)
		expect.That(t, got.Revoked <= revokeAllBatch, "a batch revoked %d, past its limit %d", got.Revoked, revokeAllBatch)
		total += got.Revoked
		if !got.More {
			break
		}
	}
	expect.That(t, total == revokeAllMatches, "the batches revoked %d, want %d", total, revokeAllMatches)
	for _, id := range ids {
		expect.That(t, f.revoked(t, id), "%s still verifies once the loop ended", id)
	}
}

// A store that scopes tenants refuses a revocation across tenants under a
// scope admitting one tenant, or reading alone, as out of scope, and
// revokes nothing.
func checkRevokeMatchingAllTenantsScope(t *testing.T, f fixture) {
	f.requireBulkRevoker(t)
	if !f.ScopesTenants {
		t.Skip("Env.ScopesTenants not set")
	}
	client := "client-" + uuid.New().String()
	c := f.insertIn(t, f.Tenant, "", func(c *limes.Capability) { c.ClientID = client })
	req := limes.RevokeMatchingRequest{AllTenants: true, ClientID: client}
	scopes := map[string]context.Context{"one tenant's scope": f.Ctx}
	if f.ReadOnlyCtx != nil {
		scopes["a read-only scope"] = f.ReadOnlyCtx
	}
	for name, ctx := range scopes {
		_, err := f.BulkRevoker.RevokeMatching(ctx, req)
		expect.That(t, errors.Is(err, limes.ErrScopeMismatch), "%s: %v, want ErrScopeMismatch", name, err)
	}
	expect.That(t, !f.revoked(t, c.ID), "a refused revocation across tenants revoked %s", c.ID)
}

// The census and the listing filter by the selectors RevokeMatching takes,
// so they preview it: a count and a listing by client, session, on-behalf-of
// or subject name the capabilities RevokeMatching then revokes, and a count
// across tenants by client names each tenant's. Each selector is a fresh
// name, as a store's other tenants may hold capabilities under the fixed
// ones.
func checkRevokeMatchingPreview(t *testing.T, f fixture) {
	f.requireBulkRevoker(t)
	if f.Census == nil || f.Lister == nil || f.Now == nil {
		t.Skip("Env.Census, Env.Lister or Env.Now not set")
	}
	other, err := f.newTenant()
	expect.Must(t, err == nil, "NewTenant: %v", err)
	client, session := "client-"+uuid.New().String(), "sid-"+uuid.New().String()
	onBehalfOf, subject := "user-"+uuid.New().String(), "agent-"+uuid.New().String()
	byClient := func(c *limes.Capability) { c.ClientID = client }
	bySessionAndClient := f.insertIn(t, f.Tenant, session, byClient)
	byClientAlone := f.insertIn(t, f.Tenant, "", byClient)
	byOnBehalfOf := f.insertIn(t, f.Tenant, "", func(c *limes.Capability) {
		c.OnBehalfOf = &limes.Principal{Type: limes.PrincipalTypeUser, TenantID: f.Tenant, Subject: onBehalfOf}
	})
	bySubject := f.insertIn(t, f.Tenant, "", func(c *limes.Capability) { c.Subject.Subject = subject })
	f.insertIn(t, f.Tenant, "", nil)
	theirs := f.insertIn(t, other, "", byClient)
	for name, tc := range map[string]struct {
		count limes.CountRecordsRequest
		list  limes.ListRecordsRequest
		want  []uuid.UUID
	}{
		"a client": {
			limes.CountRecordsRequest{TenantID: f.Tenant, ClientID: client},
			limes.ListRecordsRequest{TenantID: f.Tenant, ClientID: client},
			[]uuid.UUID{bySessionAndClient.ID, byClientAlone.ID},
		},
		"a session": {
			limes.CountRecordsRequest{TenantID: f.Tenant, Session: session},
			limes.ListRecordsRequest{TenantID: f.Tenant, Session: session},
			[]uuid.UUID{bySessionAndClient.ID},
		},
		"an on-behalf-of": {
			limes.CountRecordsRequest{TenantID: f.Tenant, OnBehalfOf: onBehalfOf},
			limes.ListRecordsRequest{TenantID: f.Tenant, OnBehalfOf: onBehalfOf},
			[]uuid.UUID{byOnBehalfOf.ID},
		},
		"a subject": {
			limes.CountRecordsRequest{TenantID: f.Tenant, Subject: subject},
			limes.ListRecordsRequest{TenantID: f.Tenant, Subject: subject},
			[]uuid.UUID{bySubject.ID},
		},
		"a client and a session": {
			limes.CountRecordsRequest{TenantID: f.Tenant, ClientID: client, Session: session},
			limes.ListRecordsRequest{TenantID: f.Tenant, ClientID: client, Session: session},
			[]uuid.UUID{bySessionAndClient.ID},
		},
	} {
		counts := f.countAll(t, f.Ctx, tc.count)
		total := int64(0)
		if len(counts) == 1 {
			total = counts[0].Total
		}
		expect.That(t, total == int64(len(tc.want)), "%s: census = %+v, want %d records", name, counts, len(tc.want))
		var listed []uuid.UUID
		for _, l := range f.listRecordsAll(t, tc.list) {
			listed = append(listed, l.Record.Capability.ID)
		}
		expect.That(t, sameIDs(listed, tc.want), "%s: listed %v, want %v", name, listed, tc.want)
	}
	across := f.countAll(t, f.allTenantsCtx(), limes.CountRecordsRequest{AllTenants: true, ByTenant: true, ClientID: client})
	want := map[uuid.UUID]int64{f.Tenant: 2, other: 1}
	got := map[uuid.UUID]int64{}
	for _, n := range across {
		got[n.TenantID] = n.Total
	}
	expect.That(t, maps.Equal(got, want), "census across tenants by client = %v, want %v", got, want)
	revoked, err := f.BulkRevoker.RevokeMatching(f.Ctx, limes.RevokeMatchingRequest{TenantID: f.Tenant, ClientID: client})
	expect.Must(t, err == nil, "RevokeMatching: %v", err)
	expect.That(t, revoked.Revoked == want[f.Tenant], "RevokeMatching revoked %d, the census previewed %d", revoked.Revoked, want[f.Tenant])
	after := f.countAll(t, f.Ctx, limes.CountRecordsRequest{TenantID: f.Tenant, ClientID: client})
	expect.That(t, len(after) == 1 && after[0].Revoked == want[f.Tenant], "census by client after the revocation = %+v, want %d revoked", after, want[f.Tenant])
	expect.That(t, !f.revoked(t, theirs.ID), "a revocation in Tenant revoked another tenant's capability")
}

// sameIDs reports whether a and b hold the same ids, in any order.
func sameIDs(a, b []uuid.UUID) bool {
	return slices.Equal(sortedTenants(a...), sortedTenants(b...))
}
