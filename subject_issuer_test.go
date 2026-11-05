package limes_test

import (
	"context"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes"
)

// Two identity providers that both name a client "shared" and a session
// "sid-1", each the issuer of one capability's subject token.
const (
	firstIdP     = "https://idp-one.example"
	secondIdP    = "https://idp-two.example"
	sharedClient = "shared"
	sharedSID    = "sid-1"
)

// The subject token's issuer an issuance names is recorded beside the
// capability, and its delegations take it; a bulk revocation by session
// that names the issuer revokes that provider's capabilities alone, where
// one without it reaches both providers'.
func TestASubjectIssuerTellsIdentityProvidersApart(t *testing.T) {
	ctx := context.Background()
	s := newExampleStack()
	issue := func(idp string) limes.IssueResult {
		t.Helper()
		issued, err := s.issuer.Issue(ctx, limes.IssueRequest{
			ClientID: sharedClient, IssuedBy: limes.Principal{Subject: "operator"}, Session: sharedSID, SubjectIssuer: idp,
			Subject:  limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: s.tenantID, Subject: "orchestrator"},
			Audience: []string{exampleAudience}, TTL: time.Hour,
			Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet, limes.OpShare}, Requests: limes.RequestCeiling{Unlimited: true}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return issued
	}
	first, second := issue(firstIdP), issue(secondIdP)
	child, err := s.issuer.Delegate(ctx, limes.DelegateRequest{
		Parent:         s.verified(first.Token),
		Subject:        limes.Principal{Type: limes.PrincipalTypeAgent, Subject: "worker"},
		InheritCaveats: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec, err := s.records.GetRecord(ctx, first.Capability.ID); err != nil || rec.SubjectIssuer != firstIdP {
		t.Errorf("the issued capability's subject issuer = %q, %v; want %q", rec.SubjectIssuer, err, firstIdP)
	}
	if rec, err := s.records.GetRecord(ctx, child.Capability.ID); err != nil || rec.SubjectIssuer != firstIdP {
		t.Errorf("its delegation's subject issuer = %q, %v; want its parent's %q", rec.SubjectIssuer, err, firstIdP)
	}
	got, err := s.records.RevokeMatching(ctx, limes.RevokeMatchingRequest{
		AllTenants: true, Session: sharedSID, SubjectIssuer: firstIdP, Actor: "idp-logout",
	})
	if err != nil || got.Revoked != 2 {
		t.Fatalf("RevokeMatching of the first provider's session = %+v, %v; want its capability and the delegation", got, err)
	}
	if revoked, err := s.records.IsRevoked(ctx, second.Capability.ID); err != nil || revoked {
		t.Errorf("the second provider's capability of the same sid was revoked: %v, %v", revoked, err)
	}
	got, err = s.records.RevokeMatching(ctx, limes.RevokeMatchingRequest{AllTenants: true, ClientID: sharedClient, Actor: "client-deleted"})
	if err != nil || got.Revoked != 1 {
		t.Errorf("RevokeMatching by client with no issuer = %+v, %v; want the second provider's one left", got, err)
	}
}
