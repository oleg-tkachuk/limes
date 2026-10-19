package limes

import (
	"context"
	"crypto"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes/internal/jose"
)

// signUnchecked signs c as Sign would, minus Sign's own checks: the token a
// key holder could mint by hand, which the verifier must judge for itself.
func signUnchecked(t *testing.T, s *Signer, c Capability) string {
	t.Helper()
	header, err := json.Marshal(jwtHeader{Alg: jose.AlgEd25519, Kid: s.keyID, Typ: string(TokenTypeV5)})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := json.Marshal(claimsOf(grant{Capability: c}))
	if err != nil {
		t.Fatal(err)
	}
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	sig, err := s.key.open().Sign(rand.Reader, []byte(input), crypto.Hash(0))
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// validCapability is one Sign takes, for each case below to spoil.
func validCapability() Capability {
	now := time.Now().UTC()
	return Capability{
		ID: uuid.New(), Issuer: "limes-test",
		Subject:  Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "a"},
		Audience: []string{audienceData}, Caveats: Caveats{Ops: []Op{OpGet}},
		IssuedAt: now, NotBefore: now, ExpiresAt: now.Add(time.Minute),
	}
}

// Caveats Issue would refuse to mint are refused wherever they turn up: Sign
// will not sign them, and the verifier will not accept a token carrying them,
// however it was signed. A negative limit would otherwise read as none.
func TestCaveatsOutsideTheRulesAreNeitherSignedNorAccepted(t *testing.T) {
	issuer, v, _, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	spoil := map[string]func(*Caveats){
		"a negative request limit":    func(c *Caveats) { c.Requests = RequestCeiling{Max: -7} },
		"a negative budget":           func(c *Caveats) { c.Budget = BudgetCeiling{Max: -NanosPerUnit} },
		"an operation nobody defined": func(c *Caveats) { c.Ops = []Op{"not an op"} },
		"an empty resource prefix":    func(c *Caveats) { c.ResourcePrefixes = []string{""} },
		"a malformed network":         func(c *Caveats) { c.SourceIPCIDR = []string{"10.0.0.0/33"} },
	}
	for name, mutate := range spoil {
		c := validCapability()
		mutate(&c.Caveats)
		if _, err := issuer.signer.sign(ctx, c); !errors.Is(err, ErrInvalidCaveats) {
			t.Errorf("Sign with %s: err = %v, want ErrInvalidCaveats", name, err)
		}
		token := signUnchecked(t, issuer.signer, c)
		// A refused token, not a malformed request: it must not also match
		// the request family a consumer answers as its own fault.
		if _, err := v.Verify(ctx, VerifyRequest{Token: token, Audience: audienceData}); !errors.Is(err, ErrInvalidToken) || errors.Is(err, ErrInvalidRequest) {
			t.Errorf("Verify of a token with %s: err = %v, want ErrInvalidToken and not ErrInvalidRequest", name, err)
		}
	}
	if _, err := v.Verify(ctx, VerifyRequest{Token: signUnchecked(t, issuer.signer, validCapability()), Audience: audienceData}); err != nil {
		t.Errorf("Verify of a well-formed token signed the same way: %v", err)
	}
}
