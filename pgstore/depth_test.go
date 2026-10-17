package pgstore

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

// depthTTL is how long the capabilities of these tests live.
const depthTTL = time.Hour

// sqlWriteRecord writes a record as the owner, past every check Insert makes.
const sqlWriteRecord = `
INSERT INTO ` + schema + `.capabilities
    (id, parent_id, tenant_id, principal_type, subject, expires_at, depth, capability, issued_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, '{}')`

// A chain deeper than MaxDelegationDepth, kept from before Insert refused
// one, is never answered for in part: the revocation check and the meter
// refuse it, rather than skip the ancestors past the bound.
func TestAChainPastMaxDelegationDepthIsRefused(t *testing.T) {
	fresh(t)
	clock := newFakeClock()
	tenant := uuid.New()
	ctx := WithTenant(t.Context(), tenant)
	var parent *uuid.UUID
	var leaf uuid.UUID
	for depth := range limes.MaxDelegationDepth + 2 {
		c := limes.Capability{
			ID: uuid.New(), ExpiresAt: clock.Now().Add(depthTTL),
			Subject: limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: tenant, Subject: "agent"},
			Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true}, UnitCode: testUnit},
		}
		if parent != nil {
			c.ParentID = *parent
		}
		raw, err := encodeCapability(c)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := owner.Exec(t.Context(), sqlWriteRecord,
			c.ID, parent, tenant, string(c.Subject.Type), c.Subject.Subject, c.ExpiresAt, depth, raw); err != nil {
			t.Fatal(err)
		}
		leaf = c.ID
		parent = &leaf
	}
	records, err := NewStore(Config{DB: app, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	usage, err := NewUsageStore(Config{DB: app, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	if revoked, err := records.IsRevoked(ctx, leaf); !errors.Is(err, limes.ErrDelegationTooDeep) {
		t.Errorf("IsRevoked = %v, %v; want ErrDelegationTooDeep", revoked, err)
	}
	subject, err := limes.StoredMeterSubject(ctx, records, leaf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := usage.Charge(ctx, limes.ChargeRequest{Subject: subject, Amount: 1}, nil); !errors.Is(err, limes.ErrDelegationTooDeep) {
		t.Errorf("Charge: err = %v, want ErrDelegationTooDeep", err)
	}
}
