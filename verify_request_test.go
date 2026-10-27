package limes

import (
	"context"
	"crypto"
	"errors"
	"net/http"
	"net/netip"
	"reflect"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes/dpop"
)

// issueBound issues a capability bound to key and returns its token.
func issueBound(t *testing.T, issuer *Issuer, key crypto.Signer) string {
	t.Helper()
	jkt, err := dpop.Thumbprint(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := issuer.issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "a"},
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
	replayed := withProof(t.Context(), t, key, bound, audienceData, now)
	if _, err := v.Verify(ctx, replayed); err != nil {
		t.Fatalf("a fresh proof from the bound key: %v", err)
	}

	// A proof made for the unbound token, presented with the bound one.
	anotherTokens := withProof(t.Context(), t, key, unbound, audienceData, now)
	anotherTokens.Token = bound
	cases := map[string]struct {
		req  VerifyRequest
		want error
	}{
		"bound, no proof":        {VerifyRequest{Token: bound, Audience: audienceData}, dpop.ErrRequired},
		"bound, another key":     {withProof(t.Context(), t, keys["P-256"], bound, audienceData, now), dpop.ErrInvalid},
		"bound, replayed proof":  {replayed, dpop.ErrReplayed},
		"bound, another token's": {anotherTokens, dpop.ErrInvalid},
		"unbound, stray proof":   {withProof(t.Context(), t, key, unbound, audienceData, now), nil},
		"no token":               {VerifyRequest{Audience: audienceData}, ErrNoCredentials},
		"no audience":            {VerifyRequest{Token: unbound}, ErrInvalidRequest},
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
	_, token, err := issuer.issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "a"},
		Audience: []string{audienceData}, Caveats: Caveats{Ops: []Op{OpGet}}, TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// A verifier with no DPoP configured refuses a key-bound capability, saying
// why, rather than accept it as a bearer token — as its own fault: the
// answer invites no proof, which it could not check.
func TestVerifyWithoutDPoPRefusesBoundTokens(t *testing.T) {
	issuer, configured, _, _ := buildIssuerVerifier(t)
	cfg := configured.cfg
	cfg.DPoP = nil
	v, err := NewVerifier(cfg)
	if err != nil {
		t.Fatal(err)
	}
	key := dpopKeys(t)["Ed25519"]
	req := withProof(t.Context(), t, key, issueBound(t, issuer, key), audienceData, time.Now())
	_, err = v.Verify(context.Background(), req)
	if !errors.Is(err, ErrProofUnsupported) || errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrProofUnsupported alone", err)
	}
	if resp := v.ErrorResponse(t.Context(), err, AuthSchemeDPoP); resp.Status != http.StatusInternalServerError || len(resp.Challenges) != 0 {
		t.Errorf("ErrorResponse = %d with %q; want 500 and no challenge", resp.Status, resp.Challenges)
	}
}

// The proof of a Biscuit is over the Biscuit as presented: one over the token
// sealed inside it is for another token.
func TestBiscuitProofIsOverTheBiscuit(t *testing.T) {
	_, v, _, _, token := biscuitFixture(t)
	key := dpopKeys(t)["Ed25519"]
	jkt, _ := dpop.Thumbprint(key.Public())
	bound, err := Attenuate(token, Attenuation{ConfirmationJKT: jkt})
	if err != nil {
		t.Fatal(err)
	}
	inner, _, err := openBiscuit(bound)
	if err != nil {
		t.Fatal(err)
	}
	req := withProof(t.Context(), t, key, inner, audienceData, time.Now())
	req.Token = bound
	if _, err := v.Verify(context.Background(), req); !errors.Is(err, dpop.ErrInvalid) {
		t.Fatalf("a proof over the sealed token: err = %v, want dpop.ErrInvalid", err)
	}
}

// Authorize is Verify and every stateless caveat, in one call.
func TestAuthorize(t *testing.T) {
	issuer, v, _, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	_, token, err := issuer.issue(ctx, IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "a"},
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
		Verify:     VerifyRequest{Token: token, Audience: audienceData},
		Check:      CheckRequest{Op: OpGet, Resource: "corpus/public/a"},
		ClientAddr: inside,
	}
	cases := map[string]struct {
		mod  func(*AuthorizeRequest)
		want error
	}{
		"allowed":                {func(*AuthorizeRequest) {}, nil},
		"another audience":       {func(r *AuthorizeRequest) { r.Verify.Audience = audienceMCP }, ErrAudienceMismatch},
		"op not allowed":         {func(r *AuthorizeRequest) { r.Check.Op = OpDelete }, ErrOpNotAllowed},
		"resource outside":       {func(r *AuthorizeRequest) { r.Check.Resource = "corpus/private/a" }, ErrResourceNotAllowed},
		"write without key":      {func(r *AuthorizeRequest) { r.Check.Op = OpPut }, ErrIdempotencyKeyRequired},
		"write with key":         {func(r *AuthorizeRequest) { r.Check.Op, r.Check.HasIdempotencyKey = OpPut, true }, nil},
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
				if c.Capability().ID == uuid.Nil() {
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

// AuthorizeRequest names its parts: an embedded request would promote its
// fields, and one later added to both parts would be an ambiguous selector —
// a compile break from an additive change.
func TestAuthorizeRequestEmbedsNothing(t *testing.T) {
	typ := reflect.TypeFor[AuthorizeRequest]()
	for i := range typ.NumField() {
		if f := typ.Field(i); f.Anonymous {
			t.Errorf("AuthorizeRequest embeds %s", f.Name)
		}
	}
}
