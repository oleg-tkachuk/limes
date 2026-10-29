package exchange

import (
	"testing"

	"github.com/oleg-tkachuk/limes"
)

// idpSession is the identity provider's session a subject token names.
const idpSession = "idp-session-9"

// The subject token's session is recorded beside the capability, so ending
// the session at the identity provider revokes what was exchanged in it.
func TestTheSessionIsRecordedAndRevokesTheCapability(t *testing.T) {
	f := newFixture(t, fixtureConfig{noDPoP: true})
	res, err := f.ex.Exchange(t.Context(), request(f.idp.sign(t, typeAccessToken, with(accessClaims(), map[string]any{claimSessionID: idpSession}))))
	if err != nil {
		t.Fatal(err)
	}
	rec, err := f.store.GetRecord(t.Context(), res.Capability.ID)
	if err != nil || rec.Session != idpSession {
		t.Fatalf("the capability's session = %q, %v; want %q", rec.Session, err, idpSession)
	}
	other, err := f.ex.Exchange(t.Context(), request(f.idp.sign(t, typeAccessToken, accessClaims())))
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.store.RevokeMatching(t.Context(), limes.RevokeMatchingRequest{TenantID: f.tenant, Session: idpSession, Actor: "backchannel-logout"})
	if err != nil || got.Revoked != 1 {
		t.Fatalf("RevokeMatching = %+v, %v; want the one capability of the session", got, err)
	}
	if revoked, err := f.store.IsRevoked(t.Context(), other.Capability.ID); err != nil || revoked {
		t.Errorf("a capability of no session was revoked: %v, %v", revoked, err)
	}
}
