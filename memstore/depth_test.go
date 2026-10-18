package memstore

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/meter"
)

// A capability deeper than MaxDelegationDepth is metered against no partial
// lineage: every call that walks its ancestors refuses it, rather than skip
// the ancestors past the bound and their ceilings with them. Insert refuses
// such a chain, so it is written here as records kept from before it did.
func TestMeteringRefusesAChainPastMaxDelegationDepth(t *testing.T) {
	ctx := context.Background()
	records := newTestStore()
	tenant := uuid.New()
	parent := uuid.Nil
	for range limes.MaxDelegationDepth + 2 {
		id := uuid.New()
		records.caps[id] = mkCap(id, parent, tenant, "agent")
		records.issuedBy[id] = limes.Principal{Subject: "op"}
		parent = id
	}
	u := newTestUsage(records)
	subject := newSubject(subjectSpec{capID: parent, tenant: tenant})
	if _, err := u.Bump(ctx, meter.BumpRequest{Subject: subject}); !errors.Is(err, limes.ErrDelegationTooDeep) {
		t.Errorf("Bump: err = %v, want ErrDelegationTooDeep", err)
	}
	if _, err := u.Charge(ctx, meter.ChargeRequest{Subject: subject, Amount: unit}, nil); !errors.Is(err, limes.ErrDelegationTooDeep) {
		t.Errorf("Charge: err = %v, want ErrDelegationTooDeep", err)
	}
	if _, err := u.Reserve(ctx, meter.ReserveRequest{Subject: subject, Amount: unit}); !errors.Is(err, limes.ErrDelegationTooDeep) {
		t.Errorf("Reserve: err = %v, want ErrDelegationTooDeep", err)
	}
	if revoked, err := records.IsRevoked(ctx, parent); !errors.Is(err, limes.ErrDelegationTooDeep) {
		t.Errorf("IsRevoked = %v, %v; want ErrDelegationTooDeep", revoked, err)
	}
}
