package exchange

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// The scopes of the tests: two asked for, one of them granted, and one
// nobody asked for.
const (
	scopeRead   = "tools:read"
	scopeList   = "tools:list"
	scopeUnsaid = "tools:admin"
)

// asked are the scopes a test request asks for.
var asked = []string{scopeRead, scopeList}

// granting is readPolicy granting scopes.
func (f *fixture) granting(scopes ...string) Policy {
	return func(ctx context.Context, g Grant) (Decision, error) {
		decision, err := f.readPolicy(ctx, g)
		decision.Scopes = scopes
		return decision, err
	}
}

// The token response's scope is what the policy granted, not what was asked
// (RFC 6749 §5.1, RFC 8693 §2.2.1): a policy narrowing the scopes asked is
// answered with the narrower ones.
func TestHandlerAnswersTheScopesGranted(t *testing.T) {
	f := newFixture(t, fixtureConfig{})
	f.ex.cfg.Policy = f.granting(scopeRead)
	form := exchangeForm(f.idp.sign(t, typeAccessToken, accessClaims()))
	form.Set(paramScope, strings.Join(asked, " "))
	w := post(t, newHandler(t, f), form, nil)
	var resp tokenResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if resp.Scope != scopeRead {
		t.Errorf("scope %q, want %q", resp.Scope, scopeRead)
	}
}

// An exchange's scopes are those the policy granted, every one asked when it
// named none; a scope nobody asked for is the policy's fault.
func TestExchangeHoldsTheGrantedScopes(t *testing.T) {
	for name, tc := range map[string]struct {
		granted []string
		want    []string
		err     error
	}{
		"none named":    {granted: nil, want: asked},
		"some of them":  {granted: []string{scopeRead}, want: []string{scopeRead}},
		"one not asked": {granted: []string{scopeRead, scopeUnsaid}, err: errPolicy},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, fixtureConfig{})
			f.ex.cfg.Policy = f.granting(tc.granted...)
			req := request(f.idp.sign(t, typeAccessToken, accessClaims()))
			req.Scopes = asked
			res, err := f.ex.Exchange(t.Context(), req)
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
			if tc.err != nil {
				if isRefusal(err) || f.store.inserts.Load() != 0 {
					t.Errorf("err = %v, %d issued; want the server's failure, nothing issued", err, f.store.inserts.Load())
				}
				return
			}
			if !slices.Equal(res.Scopes, tc.want) {
				t.Errorf("scopes %q, want %q", res.Scopes, tc.want)
			}
		})
	}
}

// Under one capability per subject token, an exchange handing back the
// capability already issued states the scopes its own policy granted.
func TestAReissuedCapabilityStatesItsScopes(t *testing.T) {
	f := newFixture(t, fixtureConfig{onePerToken: true, noDPoP: true})
	token := f.idp.sign(t, typeAccessToken, accessClaims())
	req := request(token)
	req.Scopes = asked
	if _, err := f.ex.Exchange(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	f.ex.cfg.Policy = f.granting(scopeRead)
	again, err := f.ex.Exchange(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Reissued || !slices.Equal(again.Scopes, []string{scopeRead}) {
		t.Errorf("reissued %v, scopes %q; want reissued, %q", again.Reissued, again.Scopes, scopeRead)
	}
}

// A narrowing's scopes are those its policy granted, held as an exchange's.
func TestNarrowHoldsTheGrantedScopes(t *testing.T) {
	for name, tc := range map[string]struct {
		granted []string
		want    []string
		err     error
	}{
		"none named":    {granted: nil, want: asked},
		"some of them":  {granted: []string{scopeList}, want: []string{scopeList}},
		"one not asked": {granted: []string{scopeUnsaid}, err: errPolicy},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, fixtureConfig{narrow: true})
			parent := parentFor(t, f, nil)
			f.ex.cfg.NarrowPolicy = func(ctx context.Context, n Narrowing) (NarrowDecision, error) {
				decision, err := f.narrowPolicy(ctx, n)
				decision.Scopes = tc.granted
				return decision, err
			}
			req := narrowing(parent.Token)
			req.Scopes = asked
			res, err := f.ex.Narrow(t.Context(), req)
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
			if tc.err == nil && !slices.Equal(res.Scopes, tc.want) {
				t.Errorf("scopes %q, want %q", res.Scopes, tc.want)
			}
		})
	}
}
