package limes_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes"
)

// A chain MaxDelegationDepth deep is minted, verified and revoked from its
// root; one more delegation is refused rather than minted past the reach of
// the revocation walk.
func TestDelegationStopsAtMaxDelegationDepth(t *testing.T) {
	ctx := context.Background()
	s := newExampleStack()
	root, token := s.issue()
	delegate := func(parent limes.VerifiedCapability) (limes.Capability, string, error) {
		return s.issuer.Delegate(ctx, limes.DelegateRequest{
			Parent:         parent,
			Subject:        limes.Principal{Type: limes.PrincipalAgent, TenantID: s.tenantID, Subject: "worker"},
			TTL:            time.Minute,
			InheritCaveats: true,
		})
	}
	for depth := 1; depth <= limes.MaxDelegationDepth; depth++ {
		var err error
		if _, token, err = delegate(s.verified(token)); err != nil {
			t.Fatalf("delegation %d of %d: %v", depth, limes.MaxDelegationDepth, err)
		}
	}
	leaf := s.verified(token)
	if _, _, err := delegate(leaf); !errors.Is(err, limes.ErrDelegationTooDeep) {
		t.Fatalf("a delegation past MaxDelegationDepth: err = %v, want ErrDelegationTooDeep", err)
	}

	if err := s.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: root.ID}); err != nil {
		t.Fatal(err)
	}
	_, err := s.verifier.Verify(ctx, limes.VerifyRequest{Token: token, Audience: exampleAudience})
	if !errors.Is(err, limes.ErrRevoked) {
		t.Errorf("the deepest capability after its root's revocation: err = %v, want ErrRevoked", err)
	}
}
