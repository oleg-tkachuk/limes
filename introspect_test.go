package limes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
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

func (downRevocations) RevocationLineage(context.Context, uuid.UUID) (RevocationAnswer, error) {
	return RevocationAnswer{}, errRevocationsDown
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
		Keys: staticKeys(t, map[string]ed25519.PublicKey{testKID: pub}), Revocations: downRevocations{}, AllowUnrecorded: true, TrustedIssuers: []string{"iss"},
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

// countedOutage is a revocation store, capabilities and Biscuit copies
// alike, that cannot answer and counts every question it is asked.
type countedOutage struct{ asked int }

func (o *countedOutage) IsRevoked(context.Context, uuid.UUID) (bool, error) {
	o.asked++
	return false, errRevocationsDown
}

func (o *countedOutage) RevocationLineage(context.Context, uuid.UUID) (RevocationAnswer, error) {
	o.asked++
	return RevocationAnswer{}, errRevocationsDown
}

func (o *countedOutage) IsBiscuitRevoked(context.Context, [][]byte) (bool, error) {
	o.asked++
	return false, errRevocationsDown
}

// forgedJWT is token with its signature's first character changed, so the
// signature no longer verifies.
func forgedJWT(t *testing.T, token string) string {
	t.Helper()
	sig := strings.LastIndexByte(token, '.') + 1
	if sig == 0 || sig == len(token) {
		t.Fatalf("not a JWT: %q", token)
	}
	swap := byte('A')
	if token[sig] == swap {
		swap = 'B'
	}
	return token[:sig] + string(swap) + token[sig+1:]
}

// A token of an audience the caller may not ask about is refused before the
// revocation lookup: during an outage a genuine one and a forged one get the
// same family of refusal and ask the store nothing, so the answer says
// nothing of whether a foreign token's signature verified. A token of an
// audience the caller may ask about still reaches the store, whose failure
// surfaces as the server's error.
func TestIntrospectForRefusesForeignTokensBeforeTheStore(t *testing.T) {
	const foreign = "another-service"
	pub, priv, _ := ed25519.GenerateKey(nil)
	_, jwt, err := issueClient(t, issuerOf(t, priv), testClientID)
	if err != nil {
		t.Fatal(err)
	}
	outage := &countedOutage{}
	jwtVerifier, err := NewVerifier(VerifierConfig{
		Keys: staticKeys(t, map[string]ed25519.PublicKey{testKID: pub}), Revocations: outage, AllowUnrecorded: true, TrustedIssuers: []string{"iss"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, fixture, _, _, biscuit := biscuitFixture(t)
	cfg := fixture.cfg
	cfg.Revocations, cfg.AllowUnrecorded = outage, true
	cfg.Biscuit = &BiscuitConfig{Revocations: outage, Form: fakeForm{}}
	biscuitVerifier, err := NewVerifier(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		v        *Verifier
		token    string
		mismatch bool
	}{
		{"a genuine JWT of another audience", jwtVerifier, jwt, true},
		{"a forged JWT", jwtVerifier, forgedJWT(t, jwt), false},
		{"a genuine Biscuit of another audience", biscuitVerifier, biscuit, true},
		{"what is no Biscuit", biscuitVerifier, "AAAA", false},
	} {
		outage.asked = 0
		_, err := tc.v.IntrospectFor(t.Context(), IntrospectRequest{Token: tc.token, Audiences: []string{foreign}})
		if !errors.Is(err, ErrUnauthenticated) || errors.Is(err, errRevocationsDown) {
			t.Errorf("%s: %v, want ErrUnauthenticated's family and not the store's error", tc.name, err)
		}
		if got := errors.Is(err, ErrAudienceMismatch); got != tc.mismatch {
			t.Errorf("%s: ErrAudienceMismatch = %v, want %v (%v)", tc.name, got, tc.mismatch, err)
		}
		if outage.asked != 0 {
			t.Errorf("%s: the revocation store was asked %d times, want none", tc.name, outage.asked)
		}
	}
	for name, tc := range map[string]struct {
		v     *Verifier
		token string
	}{
		"a JWT":     {jwtVerifier, jwt},
		"a Biscuit": {biscuitVerifier, biscuit},
	} {
		outage.asked = 0
		_, err := tc.v.IntrospectFor(t.Context(), IntrospectRequest{Token: tc.token, Audiences: []string{foreign, audienceData}})
		if !errors.Is(err, errRevocationsDown) || errors.Is(err, ErrUnauthenticated) {
			t.Errorf("%s of an allowed audience: %v, want the store's error and no refusal", name, err)
		}
		if outage.asked == 0 {
			t.Errorf("%s of an allowed audience: the revocation store was not asked", name)
		}
	}
}

// A token of an audience the caller may ask about is answered as Introspect
// answers it, whichever of its audiences matched.
func TestIntrospectForActive(t *testing.T) {
	_, v, _, cap, biscuit := biscuitFixture(t)
	got, err := v.IntrospectFor(t.Context(), IntrospectRequest{Token: biscuit, Audiences: []string{"another-service", audienceMCP}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Capability.ID != cap.ID || !got.FromBiscuit {
		t.Errorf("introspected %+v", got)
	}
}

// A request with no token is ErrNoCredentials; one naming no audience, or
// an empty one, is the server's error, never a refusal of the token.
func TestIntrospectForRequest(t *testing.T) {
	_, v, _, _, biscuit := biscuitFixture(t)
	for name, tc := range map[string]struct {
		req  IntrospectRequest
		want error
	}{
		"no token":          {IntrospectRequest{Audiences: []string{audienceData}}, ErrNoCredentials},
		"no audience":       {IntrospectRequest{Token: biscuit}, ErrInvalidRequest},
		"an empty audience": {IntrospectRequest{Token: biscuit, Audiences: []string{audienceData, ""}}, ErrInvalidRequest},
	} {
		if _, err := v.IntrospectFor(t.Context(), tc.req); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
}
