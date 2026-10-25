package pgstore

import (
	"testing"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

// sqlRevokeUnseen writes a revocation entry under the writer's tenant for a
// capability of another tenant, which its row-level security hides: what a
// Scope of the consumer's own could let through.
const sqlRevokeUnseen = `
INSERT INTO limes.revocations (capability_id, tenant_id, revoked_at, note, actor, cascaded)
VALUES ($1, $2, now(), 'unseen', 'test', false)`

// A revocation of a capability the writer cannot see is still notified, with
// the empty payload that clears a cache whole: naming only what the writer
// could read, the notification would name nothing and be left unsent, and a
// cache would serve the capability as live until its TTL.
func TestRevocationOfAnUnseenCapabilityIsNotified(t *testing.T) {
	f := newTenantFixture(t)
	theirs := f.parent
	theirs.ID, theirs.Subject.TenantID, theirs.Subject.Subject = uuid.New(), f.theirs, "agent:theirs"
	if err := f.records.Insert(WithTenant(t.Context(), f.theirs), limes.InsertRequest{Capability: theirs}); err != nil {
		t.Fatal(err)
	}
	next := notifications(t)
	ctx := WithTenant(t.Context(), f.mine)
	tx, err := app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if err := ScopeFromContext(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, sqlRevokeUnseen, theirs.ID, f.mine); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got, ok := next(); !ok || got != "" {
		t.Errorf("a revocation of an unseen capability: notified %q, %v; want an empty payload", got, ok)
	}
}
