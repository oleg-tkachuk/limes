package limes

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

// manyURIs is more resource URIs than a token of DefaultMaxTokenBytes holds.
const manyURIs = 1_000

func oversizedCaveats() Caveats {
	uris := make([]string, manyURIs)
	for i := range uris {
		uris[i] = fmt.Sprintf("corpus/item/%04d", i)
	}
	return Caveats{Ops: []Op{OpGet}, ResourceURIs: uris}
}

// The issuer mints no token its verifiers would refuse unread, and records
// nothing for one it refused.
func TestIssueRefusesATokenPastMaxTokenBytes(t *testing.T) {
	issuer, _, store, _ := buildIssuerVerifier(t)
	_, _, err := issuer.Issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "op"},
		Subject:  Principal{Type: PrincipalAgent, TenantID: uuid.New(), Subject: "agent"},
		Audience: []string{audienceData},
		Caveats:  oversizedCaveats(),
		TTL:      time.Minute,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
	if n := len(store.caps); n != 0 {
		t.Errorf("a refused issuance recorded %d capabilities", n)
	}
}

// Delegate and Biscuit hold their tokens to the same limit.
func TestDelegateAndBiscuitRefuseATokenPastMaxTokenBytes(t *testing.T) {
	issuer, v, store, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	wide := oversizedCaveats()
	parent, token, err := issuer.Issue(ctx, IssueRequest{
		IssuedBy: Principal{Subject: "op"},
		Subject:  Principal{Type: PrincipalAgent, TenantID: uuid.New(), Subject: "agent"},
		Audience: []string{audienceData},
		Caveats:  Caveats{Ops: []Op{OpGet}, ResourcePrefixes: []string{"corpus/"}},
		TTL:      time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	vc, err := v.Verify(ctx, VerifyRequest{Token: token, Audience: audienceData})
	if err != nil {
		t.Fatal(err)
	}
	recorded := len(store.caps)
	if _, _, err := issuer.Delegate(ctx, DelegateRequest{
		Parent: vc, Subject: Principal{Type: PrincipalAgent, Subject: "worker"}, TTL: time.Minute, Caveats: wide,
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("Delegate: err = %v, want ErrInvalidRequest", err)
	}
	if len(store.caps) != recorded {
		t.Error("a refused delegation recorded the child")
	}

	small, err := NewIssuer(IssuerConfig{Signer: issuer.signer, Store: store, IssuerName: issuer.issuerName, MaxTokenBytes: len(token)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := small.Biscuit(ctx, parent); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("Biscuit past MaxTokenBytes: err = %v, want ErrInvalidRequest", err)
	}
}
