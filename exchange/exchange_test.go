package exchange

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/dpop"
)

// An access token for the MCP server is exchanged for a capability to the
// resource asked for, which a limes verifier accepts there.
func TestExchangeIssuesACapability(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	res, err := f.ex.Exchange(t.Context(), request(f.idp.sign(t, TypeAccessToken, accessClaims())))
	if err != nil {
		t.Fatal(err)
	}
	if res.TokenType != TokenTypeBearer || !slices.Equal(res.Capability.Audience, []string{toolResource}) ||
		res.Capability.Subject.Subject != userSubject || res.Capability.ConfirmationJKT != "" || res.Capability.ClientID != idpClient {
		t.Errorf("result %+v", res)
	}
	if _, err := f.verifier.Verify(t.Context(), limes.VerifyRequest{Token: res.Token, Audience: toolResource}); err != nil {
		t.Errorf("the exchanged capability does not verify: %v", err)
	}
}

// A capability ends no later than the token exchanged for it, however long
// the policy asks for.
func TestExchangeCapsTheLifetimeAtTheSubjectToken(t *testing.T) {
	const longer = time.Hour
	f := newFixture(t, fixtureConfig{})
	f.ex.cfg.Policy = func(ctx context.Context, g Grant) (limes.IssueRequest, error) {
		issue, err := f.readPolicy(ctx, g)
		issue.TTL = longer
		return issue, err
	}
	res, err := f.ex.Exchange(t.Context(), request(f.idp.sign(t, TypeAccessToken, accessClaims())))
	if err != nil {
		t.Fatal(err)
	}
	if end := testNow.Add(subjectTTL); res.Capability.ExpiresAt.After(end) {
		t.Errorf("the capability ends at %s, after the subject token at %s", res.Capability.ExpiresAt, end)
	}
}

// A policy that asks for more than the request allows, an audience nobody
// asked for or a binding of its own, issues nothing, and the failure is the
// server's.
func TestExchangeHoldsThePolicyToTheRequest(t *testing.T) {
	for name, change := range map[string]func(*limes.IssueRequest){
		"another audience": func(r *limes.IssueRequest) { r.Audience = []string{toolResource, "https://elsewhere.example.com"} },
		"a binding":        func(r *limes.IssueRequest) { r.ConfirmationJKT = "0ZcOCORZNYy-DWpqq30jZyJGHTN0d2HglBV3uiguA4I" },
		"another to act for": func(r *limes.IssueRequest) {
			r.OnBehalfOf = &limes.Principal{Type: limes.PrincipalTypeUser, TenantID: r.Subject.TenantID, Subject: "someone-else"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, fixtureConfig{})
			f.ex.cfg.Policy = func(ctx context.Context, g Grant) (limes.IssueRequest, error) {
				issue, err := f.readPolicy(ctx, g)
				change(&issue)
				return issue, err
			}
			_, err := f.ex.Exchange(t.Context(), request(f.idp.sign(t, TypeAccessToken, accessClaims())))
			if !errors.Is(err, errPolicy) || isRefusal(err) {
				t.Errorf("err = %v, want the policy's fault", err)
			}
			if n := f.store.inserts.Load(); n != 0 {
				t.Errorf("%d capabilities issued", n)
			}
		})
	}
}

// A subject token is accepted only from a client it was issued to, and only
// while it lives; a request must name a target.
func TestExchangeRefusals(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	token := f.idp.sign(t, TypeAccessToken, accessClaims())
	elsewhere := request(token)
	elsewhere.Client.SubjectAudiences = []string{"https://other-mcp.example.com"}
	noTarget := request(token)
	noTarget.Resources = nil
	anonymous := request(token)
	anonymous.Client = Client{}
	expiring := request(f.idp.sign(t, TypeAccessToken, with(accessClaims(), map[string]any{claimExpiry: testNow.Add(time.Second / 2).Unix()})))
	for name, tc := range map[string]struct {
		req  Request
		want error
	}{
		"a token issued to another client": {elsewhere, ErrInvalidGrant},
		"no target":                        {noTarget, ErrInvalidTarget},
		"no client":                        {anonymous, ErrInvalidClient},
		"no subject token":                 {request(""), ErrInvalidRequest},
		"a token ending now":               {expiring, ErrInvalidGrant},
	} {
		if _, err := f.ex.Exchange(t.Context(), tc.req); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
	if n := f.store.inserts.Load(); n != 0 {
		t.Errorf("%d capabilities issued", n)
	}
}

// A policy's refusal is the exchange's.
func TestExchangeReturnsThePolicysRefusal(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	f.ex.cfg.Policy = func(context.Context, Grant) (limes.IssueRequest, error) { return limes.IssueRequest{}, ErrInvalidScope }
	if _, err := f.ex.Exchange(t.Context(), request(f.idp.sign(t, TypeAccessToken, accessClaims()))); !errors.Is(err, ErrInvalidScope) {
		t.Errorf("err = %v, want ErrInvalidScope", err)
	}
}

// A DPoP proof binds the capability to the client's key, and a replayed one
// issues nothing more.
func TestExchangeBindsToTheProofKey(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	key := clientKey(t)
	jkt, err := dpop.Thumbprint(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	req := request(f.idp.sign(t, TypeAccessToken, accessClaims()))
	req.DPoP = proof(t, key, "")
	res, err := f.ex.Exchange(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.TokenType != TokenTypeDPoP || res.Capability.ConfirmationJKT != jkt {
		t.Errorf("token type %s, bound to %q; want DPoP, %q", res.TokenType, res.Capability.ConfirmationJKT, jkt)
	}
	if _, err := f.ex.Exchange(t.Context(), req); !errors.Is(err, dpop.ErrReplayed) {
		t.Errorf("a replayed proof: %v, want ErrReplayed", err)
	}
	if n := f.store.inserts.Load(); n != 1 {
		t.Errorf("%d capabilities issued, want 1", n)
	}
}

// A subject token bound to a key is exchanged only with a proof by that key.
func TestExchangeHoldsABoundSubjectToItsKey(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	key, other := clientKey(t), clientKey(t)
	jkt, err := dpop.Thumbprint(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	token := f.idp.sign(t, TypeAccessToken, with(accessClaims(), map[string]any{claimCnf: map[string]any{cnfJKT: jkt}}))
	for name, tc := range map[string]struct {
		proof dpop.Proof
		want  error
	}{
		"no proof":           {dpop.Proof{}, dpop.ErrRequired},
		"a proof by another": {proof(t, other, ""), dpop.ErrInvalid},
		"a proof by its key": {proof(t, key, ""), nil},
	} {
		req := request(token)
		req.DPoP = tc.proof
		res, err := f.ex.Exchange(t.Context(), req)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
		if err == nil && res.Capability.ConfirmationJKT != jkt {
			t.Errorf("%s: bound to %q, want %q", name, res.Capability.ConfirmationJKT, jkt)
		}
	}
}

// An exchanger that checks no proofs refuses a request with one rather than
// issue a bearer capability in its place.
func TestExchangeWithoutDPoPRefusesAProof(t *testing.T) {
	f := newFixture(t, fixtureConfig{noDPoP: true})
	req := request(f.idp.sign(t, TypeAccessToken, accessClaims()))
	req.DPoP = proof(t, clientKey(t), "")
	if _, err := f.ex.Exchange(t.Context(), req); !errors.Is(err, dpop.ErrInvalid) {
		t.Errorf("err = %v, want dpop.ErrInvalid", err)
	}
}

func TestNewExchangerRefusesAnIncompleteConfig(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	full := f.ex.cfg
	for name, change := range map[string]func(*Config){
		"no issuer":   func(c *Config) { c.Issuer = nil },
		"no subjects": func(c *Config) { c.Subjects = nil },
		"no policy":   func(c *Config) { c.Policy = nil },
	} {
		cfg := full
		change(&cfg)
		if _, err := NewExchanger(cfg); err == nil {
			t.Errorf("an exchanger with %s was built", name)
		}
	}
}

// A subject token with less than minTTL left mints nothing: the capability
// would expire before it could be used. The exchanger's clock may run ahead
// of the verifier's, which still accepted the token.
func TestExchangeRefusesASubjectTokenAboutToExpire(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	claims := accessClaims()
	claims[claimExpiry] = testNow.Add(minTTL).Unix()
	f.ex.cfg.Now = func() time.Time { return testNow.Add(minTTL / 2) }
	before := f.store.inserts.Load()
	if _, err := f.ex.Exchange(t.Context(), request(f.idp.sign(t, TypeAccessToken, claims))); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("err = %v, want ErrInvalidGrant", err)
	}
	if n := f.store.inserts.Load(); n != before {
		t.Errorf("issued %d", n-before)
	}
}
