package limes

import (
	"context"
	"crypto/ed25519"
	"encoding/json/v2"
	"errors"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes/internal/jose"
)

// The subjects of the acting tests: a user, the agent acting for them, and
// the sub-agents it delegates to.
const (
	actingUser   = "user-1"
	actingAgent  = "agent-a"
	actingChild  = "agent-b"
	actingGrand  = "agent-c"
	actingTenant = "11111111-1111-1111-1111-111111111111"
)

func actingPrincipal(t *testing.T, typ PrincipalType, subject string) Principal {
	t.Helper()
	return Principal{Type: typ, TenantID: uuid.MustParse(actingTenant), Subject: subject}
}

// issueActing issues a v7 capability of holder acting on behalf of obo (nil:
// for itself), allowed to delegate.
func issueActing(t *testing.T, issuer *Issuer, holder Principal, obo *Principal) (Capability, string) {
	t.Helper()
	cap, token, err := issuer.issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: holder, OnBehalfOf: obo,
		Audience: []string{audienceData}, Caveats: Caveats{Ops: []Op{OpGet, OpShare}}, TTL: time.Hour, ClientID: testClientID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return cap, token
}

// delegateActing verifies parentToken and delegates from it to holder.
func delegateActing(t *testing.T, issuer *Issuer, v *Verifier, parentToken string, holder Principal) (Capability, string) {
	t.Helper()
	parent, err := v.Verify(t.Context(), VerifyRequest{Token: parentToken, Audience: audienceData})
	if err != nil {
		t.Fatal(err)
	}
	cap, token, err := issuer.delegate(context.Background(), DelegateRequest{Parent: parent, Subject: holder, InheritCaveats: true})
	if err != nil {
		t.Fatal(err)
	}
	return cap, token
}

// payloadOf is token's claims, decoded loosely.
func payloadOf(t *testing.T, token string) map[string]any {
	t.Helper()
	parts, err := jose.Split(token)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jose.UnB64(parts.Payload)
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

// actChain is the subjects of a token's act claim, outermost first.
func actChain(claims map[string]any) []string {
	var chain []string
	for act, _ := claims["act"].(map[string]any); act != nil; act, _ = act["act"].(map[string]any) {
		sub, _ := act["sub"].(string)
		chain = append(chain, sub)
	}
	return chain
}

// A capability acting for a user is, in a v7 token, the user's token acted
// on by the agent (RFC 8693 §4.1): sub the user, act the agent, the
// delegations nested within. limes still reads the holder as the subject.
func TestActingOnBehalfOf(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	issuer, v := issuerOf(t, priv), verifierFor(t, pub, VerifierConfig{})
	user := actingPrincipal(t, PrincipalTypeUser, actingUser)
	_, root := issueActing(t, issuer, actingPrincipal(t, PrincipalTypeAgent, actingAgent), &user)
	_, child := delegateActing(t, issuer, v, root, actingPrincipal(t, PrincipalTypeAgent, actingChild))
	_, grand := delegateActing(t, issuer, v, child, actingPrincipal(t, PrincipalTypeAgent, actingGrand))
	for name, tc := range map[string]struct {
		token  string
		holder string
		chain  []string
	}{
		"issued":            {root, actingAgent, []string{actingAgent}},
		"delegated":         {child, actingChild, []string{actingChild, actingAgent}},
		"delegated further": {grand, actingGrand, []string{actingGrand, actingChild, actingAgent}},
	} {
		claims := payloadOf(t, tc.token)
		obo, _ := claims["limes_on_behalf_of"].(map[string]any)
		if claims["sub"] != actingUser || obo["subject"] != actingUser || !slices.Equal(actChain(claims), tc.chain) {
			t.Errorf("%s: sub %v, limes_on_behalf_of %v, act %v; want %s acted on by %v", name, claims["sub"], obo, actChain(claims), actingUser, tc.chain)
		}
		got, err := v.Verify(t.Context(), VerifyRequest{Token: tc.token, Audience: audienceData})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		c := got.Capability()
		if c.Subject.Subject != tc.holder || c.OnBehalfOf == nil || c.OnBehalfOf.Subject != actingUser ||
			c.OnBehalfOf.Type != PrincipalTypeUser || !slices.Equal(c.PriorActors, tc.chain[1:]) {
			t.Errorf("%s: read back holder %q, on behalf of %+v, prior %v", name, c.Subject.Subject, c.OnBehalfOf, c.PriorActors)
		}
	}
}

// A capability acting for itself has no act, and its delegated child acts
// on behalf of its holder.
func TestActingForItself(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	issuer, v := issuerOf(t, priv), verifierFor(t, pub, VerifierConfig{})
	_, root := issueActing(t, issuer, actingPrincipal(t, PrincipalTypeService, actingAgent), nil)
	claims := payloadOf(t, root)
	if claims["sub"] != actingAgent || claims["act"] != nil || claims["limes_on_behalf_of"] != nil {
		t.Errorf("a capability for itself: sub %v, act %v, limes_on_behalf_of %v", claims["sub"], claims["act"], claims["limes_on_behalf_of"])
	}
	_, child := delegateActing(t, issuer, v, root, actingPrincipal(t, PrincipalTypeAgent, actingChild))
	claims = payloadOf(t, child)
	if claims["sub"] != actingAgent || !slices.Equal(actChain(claims), []string{actingChild}) {
		t.Errorf("its child: sub %v, act %v; want %s acted on by %s alone", claims["sub"], actChain(claims), actingAgent, actingChild)
	}
	_, grand := delegateActing(t, issuer, v, child, actingPrincipal(t, PrincipalTypeAgent, actingGrand))
	if chain := actChain(payloadOf(t, grand)); !slices.Equal(chain, []string{actingGrand, actingChild}) {
		t.Errorf("its grandchild: act %v", chain)
	}
}

// Issue refuses a principal to act for that a capability's tenant does not
// hold, or that names no one.
func TestIssueRefusesOnBehalfOf(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	issuer := issuerOf(t, priv)
	for name, obo := range map[string]Principal{
		"another tenant": {Type: PrincipalTypeUser, TenantID: uuid.New(), Subject: actingUser},
		"no subject":     actingPrincipal(t, PrincipalTypeUser, ""),
		"no type":        actingPrincipal(t, PrincipalTypeUnspecified, actingUser),
	} {
		_, _, err := issuer.issue(context.Background(), IssueRequest{
			IssuedBy: Principal{Subject: "op"}, Subject: actingPrincipal(t, PrincipalTypeAgent, actingAgent), OnBehalfOf: &obo,
			Audience: []string{audienceData}, Caveats: Caveats{Ops: []Op{OpGet}}, TTL: time.Hour, ClientID: testClientID,
		})
		if !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: %v, want ErrInvalidRequest", name, err)
		}
	}
}

// actingClaims are a v7 token's claims for agent-a acting for user-1 after
// agent-z, with a parent.
func actingClaims() map[string]any {
	claims := validClaims(uuid.MustParse(actingTenant))
	claims["sub"], claims["client_id"], claims["limes_v"] = actingUser, testClientID, formatV7
	claims["limes_principal"] = map[string]any{"type": "agent", "tenant_id": actingTenant, "subject": actingAgent}
	claims["limes_on_behalf_of"] = map[string]any{"type": "user", "tenant_id": actingTenant, "subject": actingUser}
	claims["act"] = map[string]any{"sub": actingAgent, "act": map[string]any{"sub": "agent-z"}}
	claims["limes_parent_id"] = uuid.New().String()
	return claims
}

// nestedAct is an act of depth actors.
func nestedAct(depth int) map[string]any {
	act := map[string]any{"sub": actingAgent}
	for at, i := act, 1; i < depth; i++ {
		next := map[string]any{"sub": "agent-z"}
		at["act"], at = next, next
	}
	return act
}

// A token whose sub, act and limes_on_behalf_of disagree, or whose act no
// delegation could have written, is refused.
func TestActingClaimsRefused(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	v := verifierFor(t, pub, VerifierConfig{})
	sign := func(typ string, claims map[string]any) string {
		return resign(t, priv, map[string]any{"alg": jose.AlgEd25519, "kid": testKID, "typ": typ}, claims)
	}
	if _, err := v.Verify(t.Context(), VerifyRequest{Token: sign(TokenType, actingClaims()), Audience: audienceData}); err != nil {
		t.Fatalf("the well-formed token: %v", err)
	}
	for name, edit := range map[string]func(map[string]any){
		"act without limes_on_behalf_of": func(c map[string]any) { delete(c, "limes_on_behalf_of"); c["sub"] = actingAgent },
		"limes_on_behalf_of without act": func(c map[string]any) { delete(c, "act") },
		"sub not the one acted for":      func(c map[string]any) { c["sub"] = actingAgent },
		"act not the holder":             func(c map[string]any) { c["act"] = map[string]any{"sub": "agent-z"} },
		"a prior actor with no sub": func(c map[string]any) {
			c["act"] = map[string]any{"sub": actingAgent, "act": map[string]any{"sub": ""}}
		},
		"prior actors and no parent": func(c map[string]any) { delete(c, "limes_parent_id") },
		"act deeper than delegation": func(c map[string]any) { c["act"] = nestedAct(MaxDelegationDepth + 2) },
		"act with another member":    func(c map[string]any) { c["act"] = map[string]any{"sub": actingAgent, "client_id": "x"} },
		"one acted for of another tenant": func(c map[string]any) {
			c["limes_on_behalf_of"] = map[string]any{"type": "user", "tenant_id": uuid.New(), "subject": actingUser}
		},
	} {
		claims := actingClaims()
		edit(claims)
		if _, err := v.Verify(t.Context(), VerifyRequest{Token: sign(TokenType, claims), Audience: audienceData}); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s: %v, want ErrInvalidToken", name, err)
		}
	}
	deepest := actingClaims()
	deepest["act"] = nestedAct(MaxDelegationDepth + 1)
	if _, err := v.Verify(t.Context(), VerifyRequest{Token: sign(TokenType, deepest), Audience: audienceData}); err != nil {
		t.Errorf("an act as deep as delegation goes: %v", err)
	}
}

// A clone holds its own copy of whom the capability acts for and through.
func TestCloneCopiesActing(t *testing.T) {
	obo := Principal{Type: PrincipalTypeAgent, Subject: actingUser, Agent: &AgentPrincipal{Model: "m"}}
	c := Capability{OnBehalfOf: &obo, PriorActors: []string{actingAgent}}
	d := c.Clone()
	d.OnBehalfOf.Agent.Model, d.PriorActors[0] = "changed", "changed"
	if c.OnBehalfOf.Agent.Model != "m" || c.PriorActors[0] != actingAgent {
		t.Errorf("changing a clone changed the original: %+v %v", c.OnBehalfOf.Agent, c.PriorActors)
	}
}
