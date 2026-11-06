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

// scopeCase is a policy's grant of scopes, against those asked, and how
// the exchange holds it.
type scopeCase struct {
	asked, granted, want []string
	err                  error
}

// scopeCases are the grants an exchange and a narrowing hold alike: none
// granted is none, refused when some were asked, since a response without
// scope states every scope asked (RFC 8693 §2.2.1); a scope granted twice
// is stated once; a scope nobody asked for is the policy's fault.
var scopeCases = map[string]scopeCase{
	"none granted of some asked": {asked: asked, granted: nil, err: ErrInvalidScope},
	"none granted of none asked": {asked: nil, granted: nil, want: nil},
	"some of them":               {asked: asked, granted: []string{scopeList}, want: []string{scopeList}},
	"all of them":                {asked: asked, granted: asked, want: asked},
	"one twice":                  {asked: asked, granted: []string{scopeList, scopeRead, scopeList}, want: []string{scopeList, scopeRead}},
	"one not asked":              {asked: asked, granted: []string{scopeRead, scopeUnsaid}, err: errPolicy},
	"one not asked of none":      {asked: nil, granted: []string{scopeUnsaid}, err: errPolicy},
}

func TestHoldScopes(t *testing.T) {
	for name, tc := range scopeCases {
		t.Run(name, func(t *testing.T) {
			got, err := holdScopes(tc.granted, tc.asked)
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("scopes %q, want %q", got, tc.want)
			}
		})
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

// A policy granting none of the scopes asked is answered invalid_scope,
// never a response whose missing scope would state them all; one asked for
// none is answered without a scope.
func TestHandlerAnswersNoScopeGranted(t *testing.T) {
	for name, tc := range map[string]struct {
		asked  []string
		status int
		code   string
	}{
		"some asked": {asked: asked, status: http.StatusBadRequest, code: codeInvalidScope},
		"none asked": {asked: nil, status: http.StatusOK},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, fixtureConfig{})
			f.ex.cfg.Policy = f.granting()
			form := exchangeForm(f.idp.sign(t, typeAccessToken, accessClaims()))
			if tc.asked != nil {
				form.Set(paramScope, strings.Join(tc.asked, " "))
			}
			w := post(t, newHandler(t, f), form, nil)
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.status, w.Body)
			}
			if tc.code != "" {
				var refusal Error
				if err := json.Unmarshal(w.Body.Bytes(), &refusal); err != nil || refusal.Code != tc.code {
					t.Errorf("error %q (%v), want %q", refusal.Code, err, tc.code)
				}
				return
			}
			var resp tokenResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Scope != "" {
				t.Errorf("scope %q (%v), want none", resp.Scope, err)
			}
		})
	}
}

// An exchange's scopes are those the policy granted, held as holdScopes
// holds them: a refusal or the policy's fault issues nothing.
func TestExchangeHoldsTheGrantedScopes(t *testing.T) {
	for name, tc := range scopeCases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, fixtureConfig{})
			f.ex.cfg.Policy = f.granting(tc.granted...)
			req := request(f.idp.sign(t, typeAccessToken, accessClaims()))
			req.Scopes = tc.asked
			res, err := f.ex.Exchange(t.Context(), req)
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
			if tc.err != nil {
				if isRefusal(err) != errors.Is(tc.err, ErrInvalidScope) || f.store.inserts.Load() != 0 {
					t.Errorf("err = %v (refusal %v), %d issued; want nothing issued", err, isRefusal(err), f.store.inserts.Load())
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
	for name, tc := range scopeCases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, fixtureConfig{narrow: true})
			parent := parentFor(t, f, nil)
			f.ex.cfg.NarrowPolicy = func(ctx context.Context, n Narrowing) (NarrowDecision, error) {
				decision, err := f.narrowPolicy(ctx, n)
				decision.Scopes = tc.granted
				return decision, err
			}
			req := narrowing(parent.Token)
			req.Scopes = tc.asked
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
