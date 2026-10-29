package limes_test

import (
	"context"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes"
)

// sessionID is an identity provider's session a capability is issued under.
const sessionID = "idp-session-7"

// The session an issuance names is recorded beside the capability, never
// in its token, and the capability's delegations take it; revoking the
// session revokes them all.
func TestASessionIsRecordedAndRevokedTogether(t *testing.T) {
	ctx := context.Background()
	s := newExampleStack()
	issued, err := s.issuer.Issue(ctx, limes.IssueRequest{
		ClientID: exampleClientID, IssuedBy: limes.Principal{Subject: "operator"}, Session: sessionID,
		Subject:  limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: s.tenantID, Subject: "orchestrator"},
		Audience: []string{exampleAudience}, TTL: time.Hour,
		Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet, limes.OpShare}, Requests: limes.RequestCeiling{Unlimited: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.issuer.Delegate(ctx, limes.DelegateRequest{
		Parent:         s.verified(issued.Token),
		Subject:        limes.Principal{Type: limes.PrincipalTypeAgent, Subject: "worker"},
		InheritCaveats: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec, err := s.records.GetRecord(ctx, issued.Capability.ID); err != nil || rec.Session != sessionID {
		t.Errorf("the issued capability's session = %q, %v; want %q", rec.Session, err, sessionID)
	}
	if rec, err := s.records.GetRecord(ctx, child.Capability.ID); err != nil || rec.Session != sessionID {
		t.Errorf("its delegation's session = %q, %v; want its parent's %q", rec.Session, err, sessionID)
	}
	got, err := s.records.RevokeMatching(ctx, limes.RevokeMatchingRequest{TenantID: s.tenantID, Session: sessionID, Actor: "idp-logout"})
	if err != nil || got.Revoked != 2 {
		t.Fatalf("RevokeMatching = %+v, %v; want both revoked", got, err)
	}
	if _, err := s.verifier.Verify(ctx, limes.VerifyRequest{Token: child.Token, Audience: exampleAudience}); err == nil {
		t.Error("the delegation still verifies after its session was revoked")
	}
}
