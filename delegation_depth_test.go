package limes_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes"
)

// A chain MaxDelegationDepth deep is minted, verified and revoked from its
// root; one more delegation is refused rather than minted past the reach of
// the revocation walk, and leaves no record behind.
func TestDelegationStopsAtMaxDelegationDepth(t *testing.T) {
	ctx := context.Background()
	s := newExampleStack()
	root, token := s.issue()
	delegate := func(parent limes.VerifiedCapability, subject string) (limes.Capability, string, error) {
		r, err := s.issuer.Delegate(ctx, limes.DelegateRequest{
			Parent:         parent,
			Subject:        limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: s.tenantID, Subject: subject},
			TTL:            time.Minute,
			InheritCaveats: true,
		})
		return r.Capability, r.Token, err
	}
	for depth := 1; depth <= limes.MaxDelegationDepth; depth++ {
		var err error
		if _, token, err = delegate(s.verified(token), "worker"); err != nil {
			t.Fatalf("delegation %d of %d: %v", depth, limes.MaxDelegationDepth, err)
		}
	}
	leaf := s.verified(token)
	_, _, err := delegate(leaf, "too-deep")
	if !errors.Is(err, limes.ErrDelegationDepthExceeded) {
		t.Fatalf("a delegation past MaxDelegationDepth: err = %v, want ErrDelegationDepthExceeded", err)
	}
	// The parent is live: the refusal is the delegation's, never one a
	// client would read as its token gone.
	if errors.Is(err, limes.ErrRevoked) || errors.Is(err, limes.ErrDelegationTooDeep) || errors.Is(err, limes.ErrUnauthenticated) {
		t.Errorf("a delegation past MaxDelegationDepth: err = %v matches a refusal of the parent", err)
	}
	p, ok := limes.ProblemOf(err)
	if !ok || p.Status != http.StatusForbidden || p.Code != limes.CodeDelegationDepthExceeded || p.Category != limes.CategoryDenied || p.Reason != "" {
		t.Errorf("a delegation past MaxDelegationDepth: problem %+v, %v; want 403 %s, denied, no reason", p, ok, limes.CodeDelegationDepthExceeded)
	}
	if _, err := s.verifier.Verify(ctx, limes.VerifyRequest{Token: token, Audience: exampleAudience}); err != nil {
		t.Errorf("the parent after the refused delegation: %v, want it still verified", err)
	}
	left, err := s.records.ListByPrincipal(ctx, limes.ListByPrincipalRequest{
		TenantID: s.tenantID, PrincipalType: limes.PrincipalTypeAgent, Subject: "too-deep",
	})
	if err != nil || len(left.Items) != 0 {
		t.Errorf("records of the refused delegation = %d, %v; want none", len(left.Items), err)
	}

	if _, err := s.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: root.ID}); err != nil {
		t.Fatal(err)
	}
	_, err = s.verifier.Verify(ctx, limes.VerifyRequest{Token: token, Audience: exampleAudience})
	if !errors.Is(err, limes.ErrRevoked) {
		t.Errorf("the deepest capability after its root's revocation: err = %v, want ErrRevoked", err)
	}
}
