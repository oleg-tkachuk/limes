package exchange

import (
	"context"
	"testing"

	"github.com/oleg-tkachuk/limes"
)

// The subject token's issuer is recorded beside the capability, so a bulk
// revocation by client or session names the identity provider that issued
// them; a policy naming the same issuer is taken as it is.
func TestTheSubjectIssuerIsRecorded(t *testing.T) {
	for name, policySets := range map[string]bool{"the policy names none": false, "the policy names the same": true} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, fixtureConfig{noDPoP: true})
			f.ex.cfg.Policy = func(ctx context.Context, g Grant) (Decision, error) {
				decision, err := f.readPolicy(ctx, g)
				issue := &decision.Issue
				if policySets {
					issue.SubjectIssuer = g.Subject.Issuer
				}
				return decision, err
			}
			res, err := f.ex.Exchange(t.Context(), request(f.idp.sign(t, typeAccessToken, with(accessClaims(), map[string]any{claimSessionID: idpSession}))))
			if err != nil {
				t.Fatal(err)
			}
			rec, err := f.store.GetRecord(t.Context(), res.Capability.ID)
			if err != nil || rec.SubjectIssuer != idpIssuer {
				t.Fatalf("the capability's subject issuer = %q, %v; want %q", rec.SubjectIssuer, err, idpIssuer)
			}
			got, err := f.store.RevokeMatching(t.Context(), limes.RevokeMatchingRequest{
				AllTenants: true, Session: idpSession, SubjectIssuer: "https://other-idp.example.com", Actor: "backchannel-logout",
			})
			if err != nil || got.Revoked != 0 {
				t.Fatalf("RevokeMatching of another provider's session = %+v, %v; want nothing revoked", got, err)
			}
			got, err = f.store.RevokeMatching(t.Context(), limes.RevokeMatchingRequest{
				AllTenants: true, Session: idpSession, SubjectIssuer: idpIssuer, Actor: "backchannel-logout",
			})
			if err != nil || got.Revoked != 1 {
				t.Errorf("RevokeMatching of the provider's session = %+v, %v; want the capability", got, err)
			}
		})
	}
}
