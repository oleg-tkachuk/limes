package limes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes/dpop"
)

// errRevocationsDown is a revocation store that cannot answer.
var errRevocationsDown = errors.New("revocation store down")

type downRevocations struct{}

func (downRevocations) IsRevoked(context.Context, uuid.UUID) (bool, error) {
	return false, errRevocationsDown
}

// An active token is introspected whatever audience it names and whether or
// not a key it is bound to was proved; the answer carries its capability.
func TestIntrospectActive(t *testing.T) {
	issuer, v, _, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	_, jkt := dpopKey(t)
	for name, req := range map[string]IssueRequest{
		"a bearer token": {},
		"a bound token":  {ConfirmationJKT: jkt},
	} {
		req.IssuedBy, req.ClientID = Principal{Subject: "op"}, testClientID
		req.Subject = Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "a"}
		req.Audience, req.Caveats, req.TTL = []string{"another-service"}, Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet}}, time.Hour
		cap, token, err := issuer.issue(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		got, err := v.Introspect(ctx, token)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got.Capability.ID != cap.ID || got.Capability.ConfirmationJKT != cap.ConfirmationJKT || got.FromBiscuit {
			t.Errorf("%s: introspected %+v", name, got)
		}
	}
}

// A token that is not active is a refusal of ErrUnauthenticated's family:
// expired, revoked, lifted out of its Biscuit, not a token at all.
func TestIntrospectInactive(t *testing.T) {
	issuer, v, store, cap, biscuitToken := biscuitFixture(t)
	ctx := context.Background()
	inner, _, err := fakeForm{}.Open(biscuitToken)
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.Introspect(ctx, biscuitToken)
	if err != nil || !got.FromBiscuit || got.Capability.ID != cap.ID {
		t.Fatalf("a Biscuit: %+v, %v", got, err)
	}
	_, expired, err := issuer.issue(ctx, IssueRequest{ClientID: testClientID,
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "a"},
		Audience: []string{audienceData}, Caveats: Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet}}, TTL: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	late, err := NewVerifier(VerifierConfig{
		Keys: v.cfg.Keys, Revocations: store, TrustedIssuers: v.cfg.TrustedIssuers,
		Now: func() time.Time { return time.Now().Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := late.Introspect(ctx, expired); !errors.Is(err, ErrExpired) {
		t.Errorf("an expired token: %v, want ErrExpired", err)
	}
	if _, err := store.Revoke(ctx, RevokeRequest{CapabilityID: cap.ID, Actor: "test"}); err != nil {
		t.Fatal(err)
	}
	for name, token := range map[string]string{
		"a revoked Biscuit":         biscuitToken,
		"a token lifted out of one": inner,
		"what is no token":          "a.b.c",
		"no token":                  "",
	} {
		if _, err := v.Introspect(ctx, token); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("%s: %v, want ErrUnauthenticated's family", name, err)
		}
	}
}

// A revocation store that cannot answer is the server's failure, never an
// inactive token: an outage must not read as a revocation.
func TestIntrospectOutageIsNotInactive(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	_, token, err := issueClient(t, issuerOf(t, priv), testClientID)
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewVerifier(VerifierConfig{
		Keys: staticKeys(t, map[string]ed25519.PublicKey{testKID: pub}), Revocations: downRevocations{}, TrustedIssuers: []string{"iss"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Introspect(t.Context(), token); !errors.Is(err, errRevocationsDown) || errors.Is(err, ErrUnauthenticated) {
		t.Errorf("err = %v, want the store's error and no refusal", err)
	}
}

// dpopKey is a client key and the thumbprint a capability bound to it names.
func dpopKey(t *testing.T) (ed25519.PrivateKey, string) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	jkt, err := dpop.Thumbprint(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	return key, jkt
}

// A Biscuit copy that sets limits of its own is introspected with them, on a
// verifier that meters copies, and refused as Verify refuses it on one that
// does not: no answer states the capability's wider limits as the copy's.
func TestIntrospectHoldsACopyToItsLimits(t *testing.T) {
	ctx := context.Background()
	v, _, root := limitedBiscuit(t)
	outer, err := attenuate(root, Attenuation{Requests: &RequestCeiling{Max: 50}})
	if err != nil {
		t.Fatal(err)
	}
	inner, err := attenuate(outer, Attenuation{Requests: &RequestCeiling{Max: 5}, Budget: &BudgetCeiling{Max: NanosPerUnit}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.Introspect(ctx, inner)
	if err != nil {
		t.Fatal(err)
	}
	if !got.FromBiscuit || len(got.Copies) != 2 || got.Copies[0].Requests == nil || got.Copies[0].Requests.Max != 5 ||
		got.Copies[0].Budget == nil || got.Copies[0].Budget.Max != NanosPerUnit || got.Copies[1].Requests.Max != 50 {
		t.Errorf("introspected copies %+v, want the inner block's limits first", got.Copies)
	}

	_, unmetered, _, _, token := biscuitFixture(t)
	limited, err := attenuate(token, Attenuation{Requests: &RequestCeiling{Max: 5}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unmetered.Introspect(ctx, limited); !errors.Is(err, ErrCopyCountersNotMetered) || errors.Is(err, ErrUnauthenticated) {
		t.Errorf("a limited copy without MeterCopies: %v, want ErrCopyCountersNotMetered", err)
	}
	plain, err := attenuate(token, Attenuation{Ops: []Op{OpGet}})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := unmetered.Introspect(ctx, plain); err != nil || len(got.Copies) != 0 {
		t.Errorf("a copy with no limits of its own: %+v, %v", got.Copies, err)
	}
}
