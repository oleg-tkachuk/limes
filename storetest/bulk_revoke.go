package storetest

import (
	"errors"
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
		expect.That(t, got.Revoked == int64(len(tc.want)) && !got.More, "%s: RevokeMatching = %+v, want %d revoked and no more", name, got, len(tc.want))
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
		"no tenant":        {Subject: "user"},
		"no selector":      {TenantID: f.Tenant},
		"a negative limit": {TenantID: f.Tenant, Subject: "user", Limit: -1},
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
