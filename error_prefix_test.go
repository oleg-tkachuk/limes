package limes

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"uuid"
)

// tooDeepStore answers every revocation lookup as a chain past
// MaxDelegationDepth, as a store does for one written before Insert refused it.
type tooDeepStore struct{ *memStore }

func (tooDeepStore) IsRevoked(context.Context, uuid.UUID) (bool, error) {
	return false, ErrDelegationTooDeep
}

// Every refusal names the module's prefix once, however many limes errors it
// wraps: a caller reads one "limes: ", never "limes: …: limes: …".
func TestRefusalsNameThePrefixOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tenant := uuid.New()
	issueReq := IssueRequest{ClientID: testClientID,
		IssuedBy: Principal{Subject: "test-operator"},
		Subject:  Principal{Type: PrincipalTypeAgent, TenantID: tenant, Subject: "agent"},
		Audience: []string{audienceData},
		Caveats:  Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet, OpShare}},
		TTL:      time.Hour,
	}
	revokedParent := func(t *testing.T) (*Issuer, Capability) {
		t.Helper()
		issuer, _, store, _ := buildIssuerVerifier(t)
		parent, _, err := issuer.issue(ctx, issueReq)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Revoke(ctx, RevokeRequest{CapabilityID: parent.ID}); err != nil {
			t.Fatal(err)
		}
		return issuer, parent
	}
	cases := []struct {
		name string
		want error
		run  func(t *testing.T) error
	}{
		{"reissue of an id not on record", ErrNotFound, func(t *testing.T) error {
			issuer, _, _, _ := buildIssuerVerifier(t)
			_, err := issuer.Reissue(ctx, uuid.New())
			return err
		}},
		{"reissue of a revoked capability", ErrRevoked, func(t *testing.T) error {
			issuer, parent := revokedParent(t)
			_, err := issuer.Reissue(ctx, parent.ID)
			return err
		}},
		{"delegation from a revoked parent", ErrRevoked, func(t *testing.T) error {
			issuer, parent := revokedParent(t)
			_, _, err := issuer.delegate(ctx, DelegateRequest{
				Parent: asVerified(parent), Subject: issueReq.Subject, Audience: issueReq.Audience,
				Caveats: Caveats{Requests: RequestCeiling{Max: 1}, Ops: []Op{OpGet}},
			})
			return err
		}},
		{"verification over a chain too deep", ErrDelegationTooDeep, func(t *testing.T) error {
			issuer, v, store, _ := buildIssuerVerifier(t)
			_, token, err := issuer.issue(ctx, issueReq)
			if err != nil {
				t.Fatal(err)
			}
			deep, err := NewVerifier(VerifierConfig{Keys: v.cfg.Keys, Revocations: tooDeepStore{store}, TrustedIssuers: v.cfg.TrustedIssuers})
			if err != nil {
				t.Fatal(err)
			}
			_, err = deep.Verify(ctx, VerifyRequest{Token: token, Audience: audienceData})
			return err
		}},
		{"a token whose header is not base64url", ErrInvalidToken, func(t *testing.T) error {
			_, v, _, _ := buildIssuerVerifier(t)
			_, err := v.Verify(ctx, VerifyRequest{Token: "!!!.e30.sig", Audience: audienceData})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.run(t)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if n := strings.Count(err.Error(), errorPrefix); n != 1 {
				t.Errorf("%q names %q %d times, want once", err, errorPrefix, n)
			}
		})
	}
}
