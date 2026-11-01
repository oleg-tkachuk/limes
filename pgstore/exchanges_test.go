package pgstore

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
)

// reissueTTL is the lifetime of the capability the reissue check issues.
const reissueTTL = time.Hour

// newScopedIssuer is an issuer as a token endpoint wires one: recording
// through ScopedIssuance, so no call names a tenant in its context.
func newScopedIssuer(t *testing.T, f tenantFixture) *limes.Issuer {
	t.Helper()
	key, err := limes.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := limes.NewSigner(limes.SignerConfig{KeyID: key.KeyID, Key: key.Private})
	if err != nil {
		t.Fatal(err)
	}
	iss, err := limes.NewIssuer(limes.IssuerConfig{Signer: signer, Store: ScopedIssuance(f.records), IssuerName: "limes-exchange", Now: f.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	return iss
}

// Behind a token endpoint, with no tenant in the context, an Issuer over
// ScopedIssuance reissues the capability it issued: the same token, read
// back from the record pgstore keeps, to the claim.
func TestReissueThroughScopedIssuance(t *testing.T) {
	f := newTenantFixture(t)
	iss := newScopedIssuer(t, f)
	ctx := context.Background()
	issued, err := iss.Issue(ctx, limes.IssueRequest{
		ClientID: "client", IssuedBy: limes.Principal{Subject: "operator"},
		Subject:  limes.Principal{Type: limes.PrincipalTypeUser, TenantID: f.mine, Subject: "user-1"},
		Audience: []string{"https://tools.example.com"}, TTL: reissueTTL, ConfirmationJKT: "0ZcOCORZNYy-DWpqq30jZyJGHTN0d2HglBV3uiguA4I",
		Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}, Requests: limes.RequestCeiling{Max: 1}, Budget: limes.BudgetCeiling{Max: limes.NanosPerUnit, UnitCode: testUnit}},
	})
	if err != nil {
		t.Fatal(err)
	}
	again, err := iss.Reissue(ctx, issued.Capability.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Token != issued.Token {
		t.Errorf("the reissued token differs from the issued one:\n got %s\nwant %s", again.Token, issued.Token)
	}
}

// Behind a token endpoint, ScopedExchanges claims and reads a key in the
// tenant it names, with no tenant in the context; the store itself, so
// called, is refused.
func TestScopedExchanges(t *testing.T) {
	f := newTenantFixture(t)
	ctx := context.Background()
	key := limes.ExchangeKey{TenantID: f.mine, Issuer: "https://idp.example", TokenID: "jti-1", TokenExpiresAt: time.Now().Add(time.Hour)}
	if _, _, err := f.records.ClaimExchange(ctx, key, f.child.ID); err == nil {
		t.Error("a claim with no tenant in the context went through the store's own scope check")
	}
	scoped := ScopedExchanges(f.records)
	got, claimed, err := scoped.ClaimExchange(ctx, key, f.child.ID)
	if err != nil || !claimed || got != f.child.ID {
		t.Fatalf("ClaimExchange = %s, %v, %v; want %s claimed", got, claimed, err, f.child.ID)
	}
	if got, err := scoped.ExchangedFor(ctx, key); err != nil || got != f.child.ID {
		t.Errorf("ExchangedFor = %s, %v; want %s", got, err, f.child.ID)
	}
	other := limes.ExchangeKey{TenantID: f.theirs, Issuer: key.Issuer, TokenID: key.TokenID, TokenExpiresAt: key.TokenExpiresAt}
	if _, err := scoped.ExchangedFor(ctx, other); !errors.Is(err, limes.ErrNotFound) {
		t.Errorf("the same token in another tenant: %v, want ErrNotFound", err)
	}
	if _, _, err := scoped.ClaimExchange(ctx, other, uuid.New()); !errors.Is(err, limes.ErrNotFound) {
		t.Errorf("a claim of an id not on record: %v, want ErrNotFound", err)
	}
}
