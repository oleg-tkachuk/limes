package limes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
	"time"
	"uuid"
)

// failingInsert is an IssuanceStore whose Insert fails with errInsert.
type failingInsert struct{ IssuanceStore }

var errInsert = errors.New("insert failed")

func (failingInsert) Insert(context.Context, InsertRequest) error { return errInsert }

func newTermsIssuer(t *testing.T, store IssuanceStore) *Issuer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewSigner(SignerConfig{KeyID: testKID, Key: SealKey(priv)})
	if err != nil {
		t.Fatal(err)
	}
	iss, err := NewIssuer(IssuerConfig{Biscuit: fakeForm{}, Signer: signer, Store: store, IssuerName: goldenIssuer})
	if err != nil {
		t.Fatal(err)
	}
	return iss
}

// Issue and Delegate report the first of these that fails, in this order.
func TestValidateTermsOrder(t *testing.T) {
	okAudience := []string{"api"}
	badCaveats := Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{""}}
	cases := []struct {
		name     string
		audience []string
		caveats  Caveats
		jkt      string
		want     error
	}{
		{"all valid", okAudience, Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet}}, "", nil},
		{"audience first", nil, badCaveats, "x", validateAudience(nil)},
		{"caveats before the binding", okAudience, badCaveats, "x", badCaveats.Validate()},
		{"the binding", okAudience, Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet}}, "x", validateThumbprint("x")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := validateTerms(c.audience, c.caveats, c.jkt)
			if (got == nil) != (c.want == nil) || (got != nil && got.Error() != c.want.Error()) {
				t.Errorf("validateTerms = %v, want %v", got, c.want)
			}
		})
	}
}

// A minted capability holds copies: changing the request after does not
// change it.
func TestMintCopiesTheRequest(t *testing.T) {
	iss := newTermsIssuer(t, newMemStore())
	parent := uuid.New()
	issuedAt := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	terms := grantTerms{
		Subject:   Principal{Type: PrincipalTypeService, TenantID: uuid.New(), Subject: "svc"},
		Audience:  []string{"api"},
		Caveats:   Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet}},
		IssuedAt:  issuedAt,
		ExpiresAt: issuedAt.Add(time.Hour),
		ParentID:  parent,
	}
	c := iss.mint(terms)
	terms.Audience[0] = "changed"
	terms.Caveats.Ops[0] = OpPut
	if c.Audience[0] != "api" || c.Caveats.Ops[0] != OpGet {
		t.Errorf("the minted capability changed with its request: %+v", c)
	}
	if c.ID == uuid.Nil() || c.ParentID != parent || c.Issuer != goldenIssuer {
		t.Errorf("id %s, parent %s, issuer %q", c.ID, c.ParentID, c.Issuer)
	}
	if !c.IssuedAt.Equal(issuedAt.Truncate(tokenTimeResolution)) {
		t.Errorf("IssuedAt = %s, want it at the token's resolution", c.IssuedAt)
	}
}

// A store that refuses the record fails the issue, naming what was being
// recorded, with the store's error kept.
func TestSignAndRecordNamesTheRecord(t *testing.T) {
	iss := newTermsIssuer(t, failingInsert{newMemStore()})
	c := iss.mint(grantTerms{
		Subject:  Principal{Type: PrincipalTypeService, TenantID: uuid.New(), Subject: "svc"},
		Audience: []string{"api"}, Caveats: Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet}},
		IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), ClientID: testClientID,
	})
	for _, what := range []string{"issuance", "delegation"} {
		token, err := iss.signAndRecord(t.Context(), c, Principal{Subject: "op"}, what)
		if token != "" || !errors.Is(err, errInsert) || !strings.Contains(err.Error(), "persist "+what) {
			t.Errorf("signAndRecord(%s) = %q, %v; want no token and the store's error naming %s", what, token, err, what)
		}
	}
}
