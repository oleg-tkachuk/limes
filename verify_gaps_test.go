package limes

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/google/uuid"
)

// errLookupDown is a revocation store that cannot answer.
var errLookupDown = errors.New("revocation store down")

type downLookup struct{}

func (downLookup) IsRevoked(context.Context, uuid.UUID) (bool, error) { return false, errLookupDown }

// A revocation store that cannot answer refuses the token: an unanswered
// lookup is never read as "not revoked".
func TestVerifyFailsClosedWhenRevocationCannotBeChecked(t *testing.T) {
	issuer, v, _, _ := buildIssuerVerifier(t)
	_, token, err := issuer.issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "op"},
		Subject:  Principal{Type: PrincipalAgent, TenantID: uuid.New(), Subject: "a"},
		Audience: []string{audienceData}, Caveats: Caveats{Ops: []Op{OpGet}}, TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := v.cfg
	cfg.Revocations = downLookup{}
	down, err := NewVerifier(cfg)
	if err != nil {
		t.Fatal(err)
	}
	vc, err := down.Verify(context.Background(), VerifyRequest{Token: token, Audience: audienceData})
	if !errors.Is(err, errLookupDown) || vc.Capability().ID != uuid.Nil {
		t.Errorf("Verify with the store down = %v, %v; want the store's error and no capability", vc.Capability().ID, err)
	}
}

// The validity window is honoured to the second at both ends, the leeway
// included: a token is accepted from NotBefore − Leeway to ExpiresAt +
// Leeway, and refused outside it.
func TestVerifyWindowEdges(t *testing.T) {
	issuer, v, _, _ := buildIssuerVerifier(t)
	start := time.Unix(1_800_000_000, 0).UTC()
	c := validCapability()
	c.Issuer = issuer.issuerName
	c.IssuedAt, c.NotBefore, c.ExpiresAt = start, start.Add(time.Hour), start.Add(2*time.Hour)
	token, err := issuer.signer.sign(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	leeway := v.cfg.Leeway
	for name, tc := range map[string]struct {
		now  time.Time
		want error
	}{
		"just before the window": {c.NotBefore.Add(-leeway - time.Second), ErrNotYetValid},
		"at its start":           {c.NotBefore.Add(-leeway), nil},
		"at its end":             {c.ExpiresAt.Add(leeway), nil},
		"just after it":          {c.ExpiresAt.Add(leeway + time.Second), ErrExpired},
	} {
		cfg := v.cfg
		cfg.Now = func() time.Time { return tc.now }
		at, err := NewVerifier(cfg)
		if err != nil {
			t.Fatal(err)
		}
		_, err = at.Verify(context.Background(), VerifyRequest{Token: token, Audience: audienceData})
		if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
}

// A child may not drop the idempotency key its parent requires.
func TestNarrowsKeepsIdempotency(t *testing.T) {
	parent := validCapability()
	parent.Caveats.IdempotencyKeyRequired = true
	child := parent
	child.Caveats.IdempotencyKeyRequired = false
	if err := Narrows(parent, child); !errors.Is(err, ErrDelegationTooWide) {
		t.Errorf("a child dropping IdempotencyKeyRequired: err = %v, want ErrDelegationTooWide", err)
	}
}

// A network entry that does not parse admits no one, even beside none
// that does: the check fails closed for a token minted before Validate
// refused such an entry.
func TestCheckSourceFailsClosedOnAnUnparsableNetwork(t *testing.T) {
	c := Caveats{SourceIPCIDR: []string{"not a network"}}
	if err := c.CheckSource(netip.MustParseAddr("10.0.0.1")); !errors.Is(err, ErrSourceIPNotAllowed) {
		t.Errorf("CheckSource with only an unparsable network: err = %v, want ErrSourceIPNotAllowed", err)
	}
	c.SourceIPCIDR = append(c.SourceIPCIDR, "10.0.0.0/8")
	if err := c.CheckSource(netip.MustParseAddr("10.0.0.1")); err != nil {
		t.Errorf("CheckSource inside a parsable network beside it: %v", err)
	}
}
