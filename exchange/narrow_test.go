package exchange

import (
	"context"
	"crypto"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"testing"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/dpop"
)

// parentFor is a capability exchanged for the IdP's token, addressed to the
// tool and to the exchange itself so it can be narrowed; bound to key's
// proof when key is not nil.
func parentFor(t *testing.T, f *fixture, key crypto.Signer) Result {
	t.Helper()
	f.ex.cfg.Policy = f.sharePolicy
	req := request(f.idp.sign(t, TypeAccessToken, accessClaims()))
	req.Resources = []string{toolResource, exchangeAudience}
	if key != nil {
		req.DPoP = proof(t, key, "")
	}
	res, err := f.ex.Exchange(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// sharePolicy is readPolicy with the right to delegate, which a capability
// narrowed needs.
func (f *fixture) sharePolicy(ctx context.Context, g Grant) (limes.IssueRequest, error) {
	issue, err := f.readPolicy(ctx, g)
	issue.Caveats.Ops = append(issue.Caveats.Ops, limes.OpShare)
	return issue, err
}

// narrowing is a narrowing of token to the tool alone.
func narrowing(token string) NarrowRequest {
	return NarrowRequest{Client: client(), SubjectToken: token, Resources: []string{toolResource}}
}

// overToken is a proof by key over token, as a bound capability's holder
// signs one at the token endpoint.
func overToken(t *testing.T, key crypto.Signer, token string) dpop.Proof {
	t.Helper()
	jwt, err := dpop.Sign(context.Background(), key, dpop.SignRequest{Method: http.MethodPost, URL: tokenEndpoint, Token: token, IssuedAt: testNow})
	if err != nil {
		t.Fatal(err)
	}
	return dpop.Proof{JWT: jwt, Method: http.MethodPost, URL: tokenEndpoint}
}

// A capability is narrowed to a child delegated from it, for the target
// asked for, which verifies there.
func TestNarrowDelegates(t *testing.T) {
	f := newFixture(t, fixtureConfig{narrow: true})
	parent := parentFor(t, f, nil)
	res, err := f.ex.Narrow(t.Context(), narrowing(parent.Token))
	if err != nil {
		t.Fatal(err)
	}
	if res.Capability.ParentID != parent.Capability.ID || !slices.Equal(res.Capability.Audience, []string{toolResource}) ||
		res.Capability.Subject.Subject != subAgent || res.TokenType != TokenTypeBearer {
		t.Errorf("child %+v", res.Capability)
	}
	if _, err := f.verifier.Verify(t.Context(), limes.VerifyRequest{Token: res.Token, Audience: toolResource}); err != nil {
		t.Errorf("the child does not verify: %v", err)
	}
}

// A bound parent is narrowed only with a proof of holding its key, over the
// parent's token, and its child keeps the binding.
func TestNarrowHoldsABoundParentToItsKey(t *testing.T) {
	f := newFixture(t, fixtureConfig{narrow: true})
	key, other := clientKey(t), clientKey(t)
	parent := parentFor(t, f, key)
	for name, tc := range map[string]struct {
		proof dpop.Proof
		want  error
	}{
		"no proof":             {dpop.Proof{}, dpop.ErrRequired},
		"a proof by another":   {overToken(t, other, parent.Token), dpop.ErrInvalid},
		"a token request's":    {proof(t, key, ""), dpop.ErrInvalid},
		"a proof over its own": {overToken(t, key, parent.Token), nil},
	} {
		req := narrowing(parent.Token)
		req.DPoP = tc.proof
		res, err := f.ex.Narrow(t.Context(), req)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
			continue
		}
		if err == nil && (res.Capability.ConfirmationJKT != parent.Capability.ConfirmationJKT || res.TokenType != TokenTypeDPoP) {
			t.Errorf("%s: child bound to %q as %s, want the parent's %q", name, res.Capability.ConfirmationJKT, res.TokenType, parent.Capability.ConfirmationJKT)
		}
	}
}

// An unbound parent's child is bound to the key of a token request's proof,
// recorded before the child exists.
func TestNarrowBindsAnUnboundParentsChild(t *testing.T) {
	f := newFixture(t, fixtureConfig{narrow: true})
	parent := parentFor(t, f, nil)
	key := clientKey(t)
	jkt, err := dpop.Thumbprint(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	req := narrowing(parent.Token)
	req.DPoP = proof(t, key, "")
	res, err := f.ex.Narrow(t.Context(), req)
	if err != nil || res.Capability.ConfirmationJKT != jkt || res.TokenType != TokenTypeDPoP {
		t.Fatalf("child %+v, %v; want bound to %s", res.Capability, err, jkt)
	}
	before := f.store.inserts.Load()
	if _, err := f.ex.Narrow(t.Context(), req); !errors.Is(err, dpop.ErrReplayed) {
		t.Errorf("a replayed proof: %v, want ErrReplayed", err)
	}
	if n := f.store.inserts.Load(); n != before {
		t.Errorf("a replayed proof delegated %d more", n-before)
	}
}

// A narrowing names targets within the parent's audience, of a capability
// addressed to the exchange and still standing.
func TestNarrowRefusals(t *testing.T) {
	f := newFixture(t, fixtureConfig{narrow: true})
	parent := parentFor(t, f, nil)
	outside := narrowing(parent.Token)
	outside.Resources = []string{"https://elsewhere.example.com"}
	noTarget := narrowing(parent.Token)
	noTarget.Resources = nil
	notForUs, err := f.ex.Exchange(t.Context(), request(f.idp.sign(t, TypeAccessToken, accessClaims())))
	if err != nil {
		t.Fatal(err)
	}
	f.ex.cfg.Policy = f.readPolicy
	unshared, err := f.ex.Exchange(t.Context(), Request{Client: client(), SubjectToken: f.idp.sign(t, TypeAccessToken, accessClaims()), Resources: []string{toolResource, exchangeAudience}})
	if err != nil {
		t.Fatal(err)
	}
	revoked := parentFor(t, f, nil)
	if _, err := f.store.Revoke(t.Context(), limes.RevokeRequest{CapabilityID: revoked.Capability.ID, Actor: "test"}); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		req  NarrowRequest
		want error
	}{
		"a target outside the parent's audience": {outside, ErrInvalidTarget},
		"no target":                              {noTarget, ErrInvalidTarget},
		"a capability not for the exchange":      {narrowing(notForUs.Token), ErrInvalidGrant},
		"a revoked capability":                   {narrowing(revoked.Token), ErrInvalidGrant},
		"not a capability":                       {narrowing("a.b.c"), ErrInvalidGrant},
		"a capability that may not delegate":     {narrowing(unshared.Token), ErrInvalidGrant},
		"no client":                              {NarrowRequest{SubjectToken: parent.Token, Resources: []string{toolResource}}, ErrInvalidClient},
	} {
		if _, err := f.ex.Narrow(t.Context(), tc.req); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
}

// A narrow policy asking for more than the request allows delegates nothing.
func TestNarrowHoldsThePolicyToTheRequest(t *testing.T) {
	for name, change := range map[string]func(*limes.DelegateRequest){
		"another audience": func(r *limes.DelegateRequest) { r.Audience = []string{toolResource, exchangeAudience} },
		"a binding":        func(r *limes.DelegateRequest) { r.ConfirmationJKT = "0ZcOCORZNYy-DWpqq30jZyJGHTN0d2HglBV3uiguA4I" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, fixtureConfig{narrow: true})
			parent := parentFor(t, f, nil)
			f.ex.cfg.NarrowPolicy = func(ctx context.Context, n Narrowing) (limes.DelegateRequest, error) {
				child, err := f.narrowPolicy(ctx, n)
				change(&child)
				return child, err
			}
			before := f.store.inserts.Load()
			if _, err := f.ex.Narrow(t.Context(), narrowing(parent.Token)); !errors.Is(err, errPolicy) || isRefusal(err) {
				t.Errorf("err = %v, want the policy's fault", err)
			}
			if n := f.store.inserts.Load(); n != before {
				t.Errorf("%d delegated", n-before)
			}
		})
	}
}

// An exchanger configured for no narrowing refuses one, and a config that
// narrows must say how in full.
func TestNarrowNeedsItsConfig(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	if _, err := f.ex.Narrow(t.Context(), narrowing("token")); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("err = %v, want ErrInvalidRequest", err)
	}
	full := f.ex.cfg
	for name, change := range map[string]func(*Config){
		"a verifier alone": func(c *Config) { c.Capabilities = f.verifier },
		"no audience":      func(c *Config) { c.Capabilities, c.NarrowPolicy = f.verifier, f.narrowPolicy },
		"no narrow policy": func(c *Config) { c.Capabilities, c.CapabilityAudience = f.verifier, exchangeAudience },
		"a policy alone":   func(c *Config) { c.NarrowPolicy = f.narrowPolicy },
	} {
		cfg := full
		change(&cfg)
		if _, err := NewExchanger(cfg); err == nil {
			t.Errorf("an exchanger with %s was built", name)
		}
	}
}

// The token endpoint narrows a capability given as the subject token.
func TestHandlerNarrows(t *testing.T) {
	f := newFixture(t, fixtureConfig{narrow: true})
	parent := parentFor(t, f, nil)
	form := url.Values{
		paramGrantType: {GrantTypeTokenExchange}, paramSubjectToken: {parent.Token},
		paramSubjectTokenType: {IssuedTokenTypeCapability}, paramResource: {toolResource},
	}
	w := post(t, newHandler(t, f), form, nil)
	var resp tokenResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if _, err := f.verifier.Verify(t.Context(), limes.VerifyRequest{Token: resp.AccessToken, Audience: toolResource}); err != nil {
		t.Errorf("the child does not verify: %v", err)
	}
}
