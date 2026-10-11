package limes

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes/dpop"
)

const (
	dpopURL    = "https://data.example.com/example.data.v1.ObjectService/GetObject"
	dpopMethod = "POST"
)

// withProof is a VerifyRequest for token carrying a fresh proof from key,
// issued at now.
func withProof(t *testing.T, key crypto.Signer, token, audience string, now time.Time) VerifyRequest {
	t.Helper()
	proof, err := dpop.Sign(key, dpop.ProofRequest{Method: dpopMethod, URL: dpopURL, Token: token, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return VerifyRequest{Token: token, Audience: audience, DPoP: dpop.Proof{JWT: proof, Method: dpopMethod, URL: dpopURL}}
}

func dpopKeys(t *testing.T) map[string]crypto.Signer {
	t.Helper()
	_, ed, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]crypto.Signer{"Ed25519": ed, "P-256": ec}
}

func TestKeyBindingSurvivesTheTokenAndDelegation(t *testing.T) {
	issuer, verifier, _, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	key := dpopKeys(t)["Ed25519"]
	jkt, _ := dpop.Thumbprint(key.Public())

	parent, token, err := issuer.Issue(ctx, IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{Type: PrincipalAgent, TenantID: uuid.New(), Subject: "a"},
		Audience: []string{audienceData}, Caveats: Caveats{Ops: []Op{OpGet}}, TTL: time.Hour,
		ConfirmationJKT: jkt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(ctx, VerifyRequest{Token: token, Audience: audienceData}); !errors.Is(err, dpop.ErrRequired) {
		t.Fatalf("bound token without a proof: err = %v, want dpop.ErrRequired", err)
	}
	got, err := verifier.Verify(ctx, withProof(t, key, token, audienceData, time.Now()))
	if err != nil || got.ConfirmationJKT != jkt {
		t.Fatalf("verified binding = %q, %v; want %q", got.ConfirmationJKT, err, jkt)
	}

	child, _, err := issuer.Delegate(ctx, DelegateRequest{Subject: Principal{Type: PrincipalAgent, Subject: "child"}, Parent: parent, InheritCaveats: true})
	if err != nil || child.ConfirmationJKT != jkt {
		t.Fatalf("child without its own key: binding %q, %v; want the parent's", child.ConfirmationJKT, err)
	}
	subKey := dpopKeys(t)["P-256"]
	subJKT, _ := dpop.Thumbprint(subKey.Public())
	child, _, err = issuer.Delegate(ctx, DelegateRequest{Subject: Principal{Type: PrincipalAgent, Subject: "child"}, Parent: parent, InheritCaveats: true, ConfirmationJKT: subJKT})
	if err != nil || child.ConfirmationJKT != subJKT {
		t.Fatalf("child bound to the sub-agent's key: %q, %v", child.ConfirmationJKT, err)
	}

	unbound := child
	unbound.ConfirmationJKT = ""
	if err := Narrows(parent, unbound); !errors.Is(err, ErrDelegationTooWide) {
		t.Fatalf("unbound child of a bound parent: err = %v, want ErrDelegationTooWide", err)
	}
}

func TestIssueRefusesAMalformedBinding(t *testing.T) {
	issuer, _, _, _ := buildIssuerVerifier(t)
	_, _, err := issuer.Issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{Type: PrincipalAgent, TenantID: uuid.New(), Subject: "a"},
		Audience: []string{audienceData}, Caveats: Caveats{Ops: []Op{OpGet}}, TTL: time.Hour,
		ConfirmationJKT: "not-a-thumbprint",
	})
	if err == nil {
		t.Fatal("malformed ConfirmationJKT accepted")
	}
}
