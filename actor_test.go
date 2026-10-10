package limes

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// RFC 8693 §4.1: a delegated token's sub is the authority the chain acts on,
// and its act claim nests the actors, the current one outermost. The chain
// survives the token and every step of it.
func TestDelegationIsExpressedAsAnActClaim(t *testing.T) {
	issuer, v, _, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	tenant := uuid.New()
	root, rootToken, err := issuer.Issue(ctx, IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{TenantID: tenant, Subject: "orchestrator"},
		Audience: []string{audienceData}, Caveats: Caveats{Ops: []Op{OpGet}}, TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := issuer.Delegate(ctx, DelegateRequest{Parent: root, Subject: Principal{Subject: "worker"}, InheritCaveats: true})
	if err != nil {
		t.Fatal(err)
	}
	grand, grandToken, err := issuer.Delegate(ctx, DelegateRequest{Parent: child, Subject: Principal{Subject: "sub-worker"}, InheritCaveats: true})
	if err != nil {
		t.Fatal(err)
	}

	got, err := v.Verify(ctx, VerifyRequest{Token: grandToken, Audience: audienceData})
	if err != nil {
		t.Fatal(err)
	}
	want := &Actor{Subject: "sub-worker", Actor: &Actor{Subject: "worker"}}
	if got.OnBehalfOf != "orchestrator" || !sameActor(got.Actor, want) || got.Subject.Subject != "sub-worker" {
		t.Errorf("verified grandchild acts as %+v on behalf of %q; want sub-worker after worker, on behalf of orchestrator", got.Actor, got.OnBehalfOf)
	}
	if !sameActor(grand.Actor, want) {
		t.Errorf("issued grandchild act = %+v", grand.Actor)
	}
	claims := decodeClaims(t, grandToken)
	if claims.Subject != "orchestrator" || claims.Actor == nil || claims.Actor.Subject != "sub-worker" {
		t.Errorf("wire: sub %q, act %+v", claims.Subject, claims.Actor)
	}

	rootGot, err := v.Verify(ctx, VerifyRequest{Token: rootToken, Audience: audienceData})
	if err != nil || rootGot.Actor != nil || rootGot.OnBehalfOf != "" || rootGot.Authority() != "orchestrator" {
		t.Errorf("root = act %+v on behalf of %q, %v; want no act and its own authority", rootGot.Actor, rootGot.OnBehalfOf, err)
	}
	if decodeClaims(t, rootToken).Actor != nil {
		t.Error("a root token carries an act claim")
	}
}

func sameActor(a, b *Actor) bool {
	for ; a != nil && b != nil; a, b = a.Actor, b.Actor {
		if a.Subject != b.Subject {
			return false
		}
	}
	return a == nil && b == nil
}

func decodeClaims(t *testing.T, token string) jwtClaims {
	t.Helper()
	parts, err := splitToken(token)
	if err != nil {
		t.Fatal(err)
	}
	var c jwtClaims
	if err := decodeSegment(parts.claims, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// A delegation cannot change whose authority it carries.
func TestNarrowsKeepsTheAuthority(t *testing.T) {
	p, c := narrowFixture(Caveats{Ops: []Op{OpGet}}, Caveats{Ops: []Op{OpGet}})
	p.Subject.Subject = "orchestrator"
	c.Subject.Subject, c.OnBehalfOf, c.Actor = "worker", "orchestrator", &Actor{Subject: "worker"}
	if err := Narrows(p, c); err != nil {
		t.Fatalf("a child acting for its parent's authority: %v", err)
	}
	c.OnBehalfOf = "someone-else"
	if err := Narrows(p, c); !errors.Is(err, ErrDelegationTooWide) {
		t.Fatalf("a child acting for another authority: err = %v, want ErrDelegationTooWide", err)
	}
}

// The act claim must name the principal as its current actor and nest no
// deeper than MaxDelegationDepth; a root token's sub is its principal.
func TestVerifyHoldsTheActClaimToThePrincipal(t *testing.T) {
	deep := &Actor{Subject: "s"}
	for range MaxDelegationDepth {
		deep = &Actor{Subject: "s", Actor: deep}
	}
	for name, mod := range map[string]func(map[string]any){
		"act not the principal": func(c map[string]any) { c["act"] = map[string]any{"sub": "someone-else"} },
		"act too deep":          func(c map[string]any) { c["act"] = deep },
		"root sub not the principal": func(c map[string]any) {
			c["sub"] = "someone-else"
		},
	} {
		claims := validClaims(uuid.New())
		mod(claims)
		if _, err := parseClaims(b64(mustJSON(t, claims))); err == nil {
			t.Errorf("%s: claims read", name)
		}
	}
	ok := validClaims(uuid.New())
	ok["sub"], ok["act"] = "orchestrator", map[string]any{"sub": "s", "act": map[string]any{"sub": "worker"}}
	c, err := parseClaims(b64(mustJSON(t, ok)))
	if err != nil || c.OnBehalfOf != "orchestrator" || c.Actor.Actor.Subject != "worker" {
		t.Errorf("a well-formed act claim = %+v, %v", c, err)
	}
}

// Delegation stops at MaxDelegationDepth.
func TestDelegateStopsAtTheDepthBound(t *testing.T) {
	issuer, _, _, _ := buildIssuerVerifier(t)
	c, _ := issueFor(t, issuer)
	deep := &Actor{Subject: c.Subject.Subject}
	for range MaxDelegationDepth - 1 {
		deep = &Actor{Subject: c.Subject.Subject, Actor: deep}
	}
	c.Actor, c.OnBehalfOf = deep, c.Subject.Subject
	if _, _, err := issuer.Delegate(context.Background(), DelegateRequest{Parent: c, InheritCaveats: true}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("a delegation past the bound: err = %v, want ErrInvalidRequest", err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
