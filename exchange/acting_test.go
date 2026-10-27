package exchange

import (
	"context"
	"net/url"
	"slices"
	"testing"

	"github.com/oleg-tkachuk/limes"
)

// actingAgent is the agent a policy issues a capability to, acting for the
// subject token's user.
const actingAgent = "agent:planner"

// A policy may have the capability act on behalf of the subject token's
// user; introspection then answers sub as the user, act as the agent, and a
// narrowed child's sub-agent acting for the user after the agent.
func TestExchangeActsOnBehalfOfTheSubject(t *testing.T) {
	f := newFixture(t, fixtureConfig{tokenType: limes.TokenTypeV7, narrow: true})
	f.ex.cfg.Policy = func(ctx context.Context, g Grant) (limes.IssueRequest, error) {
		issue, err := f.sharePolicy(ctx, g)
		user := issue.Subject
		issue.OnBehalfOf = &user
		issue.Subject = limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: f.tenant, Subject: actingAgent}
		return issue, err
	}
	req := request(f.idp.sign(t, TypeAccessToken, accessClaims()))
	req.Resources = []string{toolResource, exchangeAudience}
	parent, err := f.ex.Exchange(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if obo := parent.Capability.OnBehalfOf; obo == nil || obo.Subject != userSubject || parent.Capability.Subject.Subject != actingAgent {
		t.Fatalf("exchanged %+v acting for %+v", parent.Capability.Subject, parent.Capability.OnBehalfOf)
	}
	child, err := f.ex.Narrow(t.Context(), narrowing(parent.Token))
	if err != nil {
		t.Fatal(err)
	}
	h := newIntrospection(t, f.verifier)
	for name, tc := range map[string]struct {
		token string
		act   []string
	}{
		"the exchanged capability": {parent.Token, []string{actingAgent}},
		"its narrowed child":       {child.Token, []string{subAgent, actingAgent}},
	} {
		_, body := introspect(t, h, url.Values{paramToken: {tc.token}})
		var chain []string
		for a, _ := body["act"].(map[string]any); a != nil; a, _ = a["act"].(map[string]any) {
			sub, _ := a["sub"].(string)
			chain = append(chain, sub)
		}
		if body["sub"] != userSubject || !slices.Equal(chain, tc.act) {
			t.Errorf("%s: sub %v, act %v; want %s acted for by %v", name, body["sub"], chain, userSubject, tc.act)
		}
	}
}
