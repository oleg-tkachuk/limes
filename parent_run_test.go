package limes_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes"
)

// Delegate takes a child agent's ParentRunID from the parent the verifier
// accepted: it fills one the request leaves out, keeps one that agrees, and
// refuses one that names another run, and a child run that is the parent's
// own. A parent that is no agent, or names no run, gives none.
func TestDelegateDerivesTheParentRun(t *testing.T) {
	ctx := context.Background()
	s := newExampleStack()
	parentRun := uuid.New()
	verify := func(token string) limes.VerifiedCapability {
		t.Helper()
		vc, err := s.verifier.Verify(ctx, limes.VerifyRequest{Token: token, Audience: exampleAudience})
		if err != nil {
			t.Fatal(err)
		}
		return vc
	}
	issue := func(subject limes.Principal) limes.VerifiedCapability {
		t.Helper()
		subject.TenantID = s.tenantID
		r, err := s.issuer.Issue(ctx, limes.IssueRequest{
			IssuedBy: limes.Principal{Subject: "operator@example.com"},
			Subject:  subject, Audience: []string{exampleAudience}, TTL: time.Minute,
			Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet, limes.OpShare}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return verify(r.Token)
	}
	agentParent := issue(limes.Principal{Type: limes.PrincipalTypeAgent, Subject: "orchestrator", Agent: &limes.AgentPrincipal{RunID: parentRun}})
	serviceParent := issue(limes.Principal{Type: limes.PrincipalTypeService, Subject: "svc"})
	runlessParent := issue(limes.Principal{Type: limes.PrincipalTypeAgent, Subject: "runless"})

	child := func(agent *limes.AgentPrincipal) limes.Principal {
		return limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: s.tenantID, Subject: "worker", Agent: agent}
	}
	for name, tc := range map[string]struct {
		parent  limes.VerifiedCapability
		subject limes.Principal
		want    uuid.UUID
		refused bool
	}{
		"left out":                     {agentParent, child(&limes.AgentPrincipal{RunID: uuid.New()}), parentRun, false},
		"no agent details":             {agentParent, child(nil), parentRun, false},
		"the parent's run":             {agentParent, child(&limes.AgentPrincipal{ParentRunID: parentRun}), parentRun, false},
		"another run":                  {agentParent, child(&limes.AgentPrincipal{ParentRunID: uuid.New()}), uuid.Nil, true},
		"the parent's run as its own":  {agentParent, child(&limes.AgentPrincipal{RunID: parentRun}), uuid.Nil, true},
		"a service parent":             {serviceParent, child(&limes.AgentPrincipal{}), uuid.Nil, false},
		"a run under a service parent": {serviceParent, child(&limes.AgentPrincipal{ParentRunID: uuid.New()}), uuid.Nil, true},
		"a parent agent naming no run": {runlessParent, child(nil), uuid.Nil, false},
	} {
		asked := tc.subject.Agent
		var before uuid.UUID
		if asked != nil {
			before = asked.ParentRunID
		}
		r, err := s.issuer.Delegate(ctx, limes.DelegateRequest{Parent: tc.parent, Subject: tc.subject, TTL: time.Minute, InheritCaveats: true})
		if tc.refused {
			if !errors.Is(err, limes.ErrInvalidRequest) {
				t.Errorf("%s: err = %v, want ErrInvalidRequest", name, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if asked != nil && asked.ParentRunID != before {
			t.Errorf("%s: Delegate set the caller's ParentRunID to %s", name, asked.ParentRunID)
		}
		for where, c := range map[string]limes.Capability{"minted": r.Capability, "verified": verify(r.Token).Capability()} {
			got := uuid.Nil
			if c.Subject.Agent != nil {
				got = c.Subject.Agent.ParentRunID
			}
			if got != tc.want {
				t.Errorf("%s: %s child's ParentRunID = %s, want %s", name, where, got, tc.want)
			}
		}
	}
}
