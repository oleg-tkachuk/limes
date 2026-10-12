package memstore

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

// A capability deeper than MaxDelegationDepth is metered against no partial
// lineage: every call that walks its ancestors refuses it, rather than skip
// the ancestors past the bound and their ceilings with them.
func TestMeteringRefusesAChainPastMaxDelegationDepth(t *testing.T) {
	ctx := context.Background()
	records := New[struct{}]()
	tenant := uuid.New()
	parent := uuid.Nil
	for range limes.MaxDelegationDepth + 2 {
		id := uuid.New()
		if err := records.Insert(ctx, limes.InsertRequest{Capability: mkCap(id, parent, tenant, "agent"), IssuedBy: limes.Principal{Subject: "op"}}); err != nil {
			t.Fatal(err)
		}
		parent = id
	}
	u := NewUsage[struct{}](records)
	subject := newSubject(subjectSpec{capID: parent, tenant: tenant})
	if _, err := u.Bump(ctx, limes.BumpRequest{Subject: subject}); !errors.Is(err, limes.ErrDelegationTooDeep) {
		t.Errorf("Bump: err = %v, want ErrDelegationTooDeep", err)
	}
	if _, err := u.Charge(ctx, limes.ChargeRequest{Subject: subject, Amount: unit}, nil); !errors.Is(err, limes.ErrDelegationTooDeep) {
		t.Errorf("Charge: err = %v, want ErrDelegationTooDeep", err)
	}
	if _, err := u.Reserve(ctx, limes.ReserveRequest{Subject: subject, Amount: unit}); !errors.Is(err, limes.ErrDelegationTooDeep) {
		t.Errorf("Reserve: err = %v, want ErrDelegationTooDeep", err)
	}
	if revoked, err := records.IsRevoked(ctx, parent); !errors.Is(err, limes.ErrDelegationTooDeep) {
		t.Errorf("IsRevoked = %v, %v; want ErrDelegationTooDeep", revoked, err)
	}
}
