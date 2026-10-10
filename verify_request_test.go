package limes

import (
	"context"
	"crypto"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/google/uuid"
)

// issueBound issues a capability bound to key and returns its token.
func issueBound(t *testing.T, issuer *Issuer, key crypto.Signer) string {
	t.Helper()
	jkt, err := KeyThumbprint(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := issuer.Issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{TenantID: uuid.New(), Subject: "a"},
		Audience: []string{audienceData}, Caveats: Caveats{Ops: []Op{OpGet}}, TTL: time.Hour, ConfirmationJKT: jkt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// Verify enforces the binding itself: a key-bound token is never accepted as
// a bearer token, whatever the caller forgets.
func TestVerifyEnforcesProofOfPossession(t *testing.T) {
	issuer, v, _, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	keys := dpopKeys(t)
	key := keys["Ed25519"]
	bound := issueBound(t, issuer, key)
	unbound := mintFor(t, issuer)
	now := time.Now()
	replayed := withProof(t, key, bound, audienceData, now)
	if _, err := v.Verify(ctx, replayed); err != nil {
		t.Fatalf("a fresh proof from the bound key: %v", err)
	}

	cases := map[string]struct {
		req  VerifyRequest
		want error
	}{
		"bound, no proof":        {VerifyRequest{Token: bound, Audience: audienceData}, ErrDPoPRequired},
		"bound, another key":     {withProof(t, keys["P-256"], bound, audienceData, now), ErrDPoPInvalid},
		"bound, replayed proof":  {replayed, ErrDPoPReplayed},
		"bound, another token's": {func() VerifyRequest { r := withProof(t, key, unbound, audienceData, now); r.Token = bound; return r }(), ErrDPoPInvalid},
		"unbound, stray proof":   {withProof(t, key, unbound, audienceData, now), nil},
		"no token":               {VerifyRequest{Audience: audienceData}, ErrInvalidSignature},
		"no audience":            {VerifyRequest{Token: unbound}, ErrAudienceMismatch},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := v.Verify(ctx, tc.req)
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func mintFor(t *testing.T, issuer *Issuer) string {
	t.Helper()
	_, token, err := issuer.Issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{TenantID: uuid.New(), Subject: "a"},
		Audience: []string{audienceData}, Caveats: Caveats{Ops: []Op{OpGet}}, TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// A verifier with no DPoP configured refuses a key-bound capability, saying
// why, rather than accept it as a bearer token.
func TestVerifyWithoutDPoPRefusesBoundTokens(t *testing.T) {
	issuer, configured, _, _ := buildIssuerVerifier(t)
	cfg := configured.cfg
	cfg.DPoP = nil
	v, err := NewStandardVerifier(cfg)
	if err != nil {
		t.Fatal(err)
	}
	key := dpopKeys(t)["Ed25519"]
	req := withProof(t, key, issueBound(t, issuer, key), audienceData, time.Now())
	if _, err := v.Verify(context.Background(), req); !errors.Is(err, ErrDPoPRequired) {
		t.Fatalf("err = %v, want ErrDPoPRequired", err)
	}
}

func TestNewStandardVerifierRequiresAReplayCache(t *testing.T) {
	_, configured, _, _ := buildIssuerVerifier(t)
	cfg := configured.cfg
	cfg.DPoP = &DPoPVerifier{}
	if _, err := NewStandardVerifier(cfg); !errors.Is(err, errNoReplayCache) {
		t.Fatalf("err = %v, want errNoReplayCache", err)
	}
}

// The proof of a Biscuit is over the Biscuit as presented: one over the token
// sealed inside it is for another token.
func TestBiscuitProofIsOverTheBiscuit(t *testing.T) {
	_, v, _, _, token := biscuitFixture(t)
	key := dpopKeys(t)["Ed25519"]
	jkt, _ := KeyThumbprint(key.Public())
	bound, err := Attenuate(token, Attenuation{ConfirmationJKT: jkt})
	if err != nil {
		t.Fatal(err)
	}
	inner, _, err := openBiscuit(bound)
	if err != nil {
		t.Fatal(err)
	}
	req := withProof(t, key, inner, audienceData, time.Now())
	req.Token = bound
	if _, err := v.Verify(context.Background(), req); !errors.Is(err, ErrDPoPInvalid) {
		t.Fatalf("a proof over the sealed token: err = %v, want ErrDPoPInvalid", err)
	}
}

// Authorize is Verify and every stateless caveat, in one call.
func TestAuthorize(t *testing.T) {
	issuer, v, _, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	_, token, err := issuer.Issue(ctx, IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{TenantID: uuid.New(), Subject: "a"},
		Audience: []string{audienceData}, TTL: time.Hour,
		Caveats: Caveats{
			Ops: []Op{OpGet, OpPut}, ResourcePrefixes: []string{"corpus/public"},
			IdempotencyKeyRequired: true, SourceIPCIDR: []string{"10.0.0.0/8"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	inside := netip.MustParseAddr("10.1.2.3")
	ok := AuthorizeRequest{
		VerifyRequest: VerifyRequest{Token: token, Audience: audienceData},
		CheckRequest:  CheckRequest{Op: OpGet, Resource: "corpus/public/a"},
		ClientAddr:    inside,
	}
	cases := map[string]struct {
		mod  func(*AuthorizeRequest)
		want error
	}{
		"allowed":                {func(*AuthorizeRequest) {}, nil},
		"another audience":       {func(r *AuthorizeRequest) { r.Audience = audienceMCP }, ErrAudienceMismatch},
		"op not allowed":         {func(r *AuthorizeRequest) { r.Op = OpDelete }, ErrOpNotAllowed},
		"resource outside":       {func(r *AuthorizeRequest) { r.Resource = "corpus/private/a" }, ErrResourceNotAllowed},
		"write without key":      {func(r *AuthorizeRequest) { r.Op = OpPut }, ErrIdempotencyKeyRequired},
		"write with key":         {func(r *AuthorizeRequest) { r.Op, r.HasIdempotencyKey = OpPut, true }, nil},
		"client outside":         {func(r *AuthorizeRequest) { r.ClientAddr = netip.MustParseAddr("192.0.2.1") }, ErrSourceIPNotAllowed},
		"client address unknown": {func(r *AuthorizeRequest) { r.ClientAddr = netip.Addr{} }, ErrSourceIPNotAllowed},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			req := ok
			tc.mod(&req)
			c, err := v.Authorize(ctx, req)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				if c.ID == uuid.Nil {
					t.Fatal("no capability returned")
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}
