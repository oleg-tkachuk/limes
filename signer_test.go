package limes

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes/internal/jose"
)

// TestSign_RoundTrip confirms Sign produces a token that Decode parses
// back into a Capability with the same load-bearing fields.
func TestSign_RoundTrip(t *testing.T) {
	t.Parallel()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := NewSigner(SignerConfig{KeyID: testKID, Key: SealKey(priv)})
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}

	tenantID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	cap := Capability{ClientID: testClientID,
		ID:     uuid.New(),
		Issuer: "limes-test",
		Subject: Principal{
			Type:     PrincipalTypeAgent,
			TenantID: tenantID,
			Subject:  "agent-42",
			Agent: &AgentPrincipal{
				AgentType:    "research-orchestrator",
				AgentVersion: "1.0.0",
				RunID:        uuid.New(),
				Model:        "example-model-1",
			},
		},
		Audience:  []string{audienceData, audienceMCP},
		IssuedAt:  now,
		ExpiresAt: now.Add(15 * time.Minute),
		Caveats: Caveats{
			Ops:              []Op{OpGet, OpList, "index:search"},
			ResourcePrefixes: []string{"object://acme/run-42/"},
			Budget:           BudgetCeiling{Max: NanosPerUnit / 2, UnitCode: "USD"},
			Requests:         RequestCeiling{Max: 100},
		},
	}

	token, err := signer.sign(context.Background(), cap)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := verifySignature(token, pub); err != nil {
		t.Fatalf("verify signature: %v", err)
	}

	got, err := decode(token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != cap.ID {
		t.Errorf("ID: got %s, want %s", got.ID, cap.ID)
	}
	if got.Issuer != cap.Issuer {
		t.Errorf("Issuer: got %s, want %s", got.Issuer, cap.Issuer)
	}
	if got.Subject.TenantID != cap.Subject.TenantID {
		t.Errorf("TenantID round-trip mismatch")
	}
	if got.Subject.Agent == nil || got.Subject.Agent.AgentType != "research-orchestrator" {
		t.Errorf("AgentPrincipal round-trip lost")
	}
	if got.Caveats.Budget != (BudgetCeiling{Max: NanosPerUnit / 2, UnitCode: "USD"}) {
		t.Errorf("MaxBudgetAmount: got %v, want 0.50 USD", got.Caveats.Budget)
	}
	if !got.IssuedAt.Equal(cap.IssuedAt) {
		t.Errorf("IssuedAt round-trip drift: %s vs %s", got.IssuedAt, cap.IssuedAt)
	}
}

// TestSign_RejectsInvalid covers the validation guards in Sign.
func TestSign_RejectsInvalid(t *testing.T) {
	t.Parallel()

	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := NewSigner(SignerConfig{KeyID: testKID, Key: SealKey(priv)})

	if _, err := signer.sign(context.Background(), signableCapability()); err != nil {
		t.Fatalf("sign(signableCapability()) = %v, want a token", err)
	}
	// Each case breaks one thing in a capability that signs, and is refused
	// for that thing: the error names it.
	cases := map[string]struct {
		edit func(c *Capability)
		want string
	}{
		"missing ID":        {func(c *Capability) { c.ID = uuid.Nil() }, "ID required"},
		"missing issuer":    {func(c *Capability) { c.Issuer = "" }, "Issuer required"},
		"missing tenant":    {func(c *Capability) { c.Subject.TenantID = uuid.Nil() }, "TenantID required"},
		"missing subject":   {func(c *Capability) { c.Subject.Subject = "" }, "Subject.Subject required"},
		"empty ops":         {func(c *Capability) { c.Caveats.Ops = nil }, "Op required"},
		"invalid caveats":   {func(c *Capability) { c.Caveats.Ops = []Op{""} }, "invalid caveats"},
		"invalid subject":   {func(c *Capability) { c.Subject.Type = "" }, "principal"},
		"reversed times":    {func(c *Capability) { c.ExpiresAt = c.IssuedAt.Add(-time.Minute) }, "ExpiresAt must be after"},
		"no expiry":         {func(c *Capability) { c.IssuedAt, c.ExpiresAt = time.Time{}, time.Time{} }, "ExpiresAt must be after"},
		"missing client id": {func(c *Capability) { c.ClientID = "" }, "ClientID"},
		"prior actors acting for itself": {
			func(c *Capability) { c.PriorActors = []string{"agent"} }, "PriorActors need an OnBehalfOf",
		},
		"acting for another tenant": {
			func(c *Capability) {
				c.OnBehalfOf = &Principal{Type: PrincipalTypeUser, TenantID: uuid.New(), Subject: "u"}
			},
			"OnBehalfOf is of tenant",
		},
		"prior actors and no parent": {
			func(c *Capability) { c.OnBehalfOf, c.PriorActors = onBehalfOfHolder(c), []string{"agent"} }, "PriorActors need a ParentID",
		},
		"an empty prior actor": {
			func(c *Capability) {
				c.OnBehalfOf, c.PriorActors, c.ParentID = onBehalfOfHolder(c), []string{""}, uuid.New()
			},
			"PriorActors are at most",
		},
		"prior actors past MaxDelegationDepth": {
			func(c *Capability) {
				c.OnBehalfOf, c.ParentID = onBehalfOfHolder(c), uuid.New()
				c.PriorActors = slices.Repeat([]string{"agent"}, MaxDelegationDepth+1)
			},
			"PriorActors are at most",
		},
		"time beyond I-JSON": {
			func(c *Capability) { c.ExpiresAt = time.Unix(MaxIJSONInteger+1, 0) }, "I-JSON",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := signableCapability()
			tc.edit(&c)
			if _, err := signer.sign(context.Background(), c); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("sign = %v, want an error naming %q", err, tc.want)
			}
		})
	}
}

// TestSign_TamperDetection mutates the encoded token mid-stream and
// confirms VerifySignature rejects.
func TestSign_TamperDetection(t *testing.T) {
	t.Parallel()

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := NewSigner(SignerConfig{KeyID: testKID, Key: SealKey(priv)})

	now := time.Now()
	cap := Capability{
		ID:        uuid.New(),
		Issuer:    "i",
		Subject:   Principal{TenantID: uuid.New(), Type: PrincipalTypeAgent, Subject: "agent"},
		Audience:  []string{audienceData},
		Caveats:   Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet}},
		IssuedAt:  now,
		ExpiresAt: now.Add(time.Minute),
		ClientID:  testClientID,
	}
	token, err := signer.sign(context.Background(), cap)
	if err != nil {
		t.Fatal(err)
	}

	// Flip a byte in the middle of the payload segment.
	mid := len(token) / 2
	tampered := token[:mid] + flipByte(token[mid]) + token[mid+1:]
	if err := verifySignature(tampered, pub); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("tampered token verified: err=%v", err)
	}
}

func flipByte(b byte) string {
	if b == 'a' {
		return "b"
	}
	return "a"
}

// TestNarrows_RejectsWidening confirms the delegation guard catches
// the typical widening attempts: extra ops, longer TTL, larger budget,
// extra audience, tainted-read upgrade.
func TestNarrows_RejectsWidening(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	now := time.Now().UTC()

	parent := Capability{ClientID: testClientID,
		ID:       uuid.New(),
		Issuer:   "i",
		Subject:  Principal{TenantID: tenantID, Type: PrincipalTypeAgent, Subject: "agent"},
		Audience: []string{audienceData},
		Caveats: Caveats{
			Ops:              []Op{OpGet, OpList},
			ResourcePrefixes: []string{"object://acme/"},
			Budget:           BudgetCeiling{Max: MustParseAmount("1.00")},
			Requests:         RequestCeiling{Max: 50},
			Copies:           CopyQuota{Max: 4},
		},
		IssuedAt:  now,
		ExpiresAt: now.Add(15 * time.Minute),
	}

	cases := map[string]func(c *Capability){
		"more copies": func(c *Capability) {
			c.Caveats.Copies = CopyQuota{Max: 5}
		},
		"unlimited copies": func(c *Capability) {
			c.Caveats.Copies = CopyQuota{Unlimited: true}
		},
		"extra op": func(c *Capability) {
			c.Caveats.Ops = append(c.Caveats.Ops, OpDelete)
		},
		"extra audience": func(c *Capability) {
			c.Audience = append(c.Audience, audienceAdmin)
		},
		"longer ttl": func(c *Capability) {
			c.ExpiresAt = now.Add(time.Hour)
		},
		"wider budget": func(c *Capability) {
			c.Caveats.Budget = BudgetCeiling{Max: 10 * NanosPerUnit}
		},
		"unrestricted prefix": func(c *Capability) {
			c.Caveats.ResourcePrefixes = nil
		},
		"escapes prefix": func(c *Capability) {
			c.Caveats.ResourcePrefixes = []string{"object://other/"}
		},
		"tainted upgrade": func(c *Capability) {
			c.Caveats.AllowTaintedRead = true
		},
		"unlimited requests": func(c *Capability) {
			c.Caveats.Requests = RequestCeiling{Unlimited: true}
		},
		"unlimited budget": func(c *Capability) {
			c.Caveats.Budget = BudgetCeiling{Unlimited: true, UnitCode: "USD"}
		},
		"cross tenant": func(c *Capability) {
			c.Subject.TenantID = uuid.New()
		},
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			child := parent
			child.ID = uuid.New()
			child.ParentID = parent.ID
			child.IssuedAt = now
			child.ExpiresAt = now.Add(5 * time.Minute)
			child.Caveats = parent.Caveats
			child.Audience = append([]string(nil), parent.Audience...)
			mut(&child)
			if err := Narrows(parent, child); err == nil {
				t.Fatalf("expected narrowing error for %s, got nil", name)
			}
		})
	}
}

// TestNarrows_RejectsCrossCurrency confirms a child capability whose
// UnitCode disagrees with the parent's is rejected at delegation
// time. We don't auto-convert between currencies — a child in EUR
// off a USD parent has no defined budget semantics, so the issuer
// fails closed.
func TestNarrows_RejectsCrossCurrency(t *testing.T) {
	t.Parallel()
	tenantID := uuid.New()
	now := time.Now().UTC()
	parent := Capability{ClientID: testClientID,
		ID:       uuid.New(),
		Issuer:   "i",
		Subject:  Principal{TenantID: tenantID, Type: PrincipalTypeAgent, Subject: "agent"},
		Audience: []string{audienceData},
		Caveats: Caveats{
			Requests: RequestCeiling{Unlimited: true},
			Ops:      []Op{OpGet},
			Budget:   BudgetCeiling{Max: MustParseAmount("10.0"), UnitCode: "USD"},
		},
		IssuedAt:  now,
		ExpiresAt: now.Add(15 * time.Minute),
	}
	child := parent
	child.ID = uuid.New()
	child.ParentID = parent.ID
	child.Caveats = Caveats{
		Requests: RequestCeiling{Unlimited: true},
		Ops:      []Op{OpGet},
		Budget:   BudgetCeiling{Max: MustParseAmount("5.0")},
	}
	// No unit is assumed for an empty one: it matches only another empty.
	for _, tc := range []struct {
		parent, child string
		ok            bool
	}{
		{"USD", "EUR", false},
		{"", "USD", false},
		{"USD", "", false},
		{"", "", true},
		{"USD", "USD", true},
	} {
		parent.Caveats.Budget.UnitCode, child.Caveats.Budget.UnitCode = tc.parent, tc.child
		err := Narrows(parent, child)
		if got := !errors.Is(err, ErrUnitCodeMismatch); got != tc.ok {
			t.Errorf("Narrows, parent in %q, child in %q: err = %v; want ok=%v", tc.parent, tc.child, err, tc.ok)
		}
	}
}

// TestNarrows_AcceptsValidNarrowing covers the happy path: narrowing on
// every axis produces a valid child.
func TestNarrows_AcceptsValidNarrowing(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	now := time.Now().UTC()
	parent := Capability{ClientID: testClientID,
		ID:       uuid.New(),
		Issuer:   "i",
		Subject:  Principal{TenantID: tenantID, Type: PrincipalTypeAgent, Subject: "agent"},
		Audience: []string{audienceData, audienceMCP},
		Caveats: Caveats{
			Ops:              []Op{OpGet, OpList, "index:search"},
			ResourcePrefixes: []string{"object://acme/"},
			Budget:           BudgetCeiling{Max: MustParseAmount("1.00")},
			Requests:         RequestCeiling{Max: 100},
		},
		IssuedAt:  now,
		ExpiresAt: now.Add(time.Hour),
	}
	child := Capability{ClientID: testClientID,
		ID:       uuid.New(),
		Issuer:   "i",
		Subject:  Principal{TenantID: tenantID, Type: PrincipalTypeAgent, Subject: "agent"},
		Audience: []string{audienceData},
		Caveats: Caveats{
			Ops:              []Op{OpGet},
			ResourcePrefixes: []string{"object://acme/run-42/"},
			Budget:           BudgetCeiling{Max: NanosPerUnit / 10},
			Requests:         RequestCeiling{Max: 10},
		},
		IssuedAt:  now,
		ExpiresAt: now.Add(15 * time.Minute),
		ParentID:  parent.ID,
	}
	if err := Narrows(parent, child); err != nil {
		t.Fatalf("expected narrowing OK, got %v", err)
	}
}

// ctxKey is an ed25519 key behind ContextSigner, recording the context of
// each signature, as a KMS-backed key would use it for its call.
type ctxKey struct {
	ed25519.PrivateKey
	got []context.Context
}

func (k *ctxKey) SignContext(ctx context.Context, rand io.Reader, msg []byte, opts crypto.SignerOpts) ([]byte, error) {
	k.got = append(k.got, ctx)
	return k.Sign(rand, msg, opts)
}

type requestValue struct{}

// The issuer signs under the caller's context, so a slow or cancelled KMS
// call is bounded by the request that asked for it.
func TestIssuerSignsUnderTheCallersContext(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key := &ctxKey{PrivateKey: priv}
	signer, err := NewSigner(SignerConfig{KeyID: testKID, Key: SealKey(key)})
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := NewIssuer(IssuerConfig{Biscuit: fakeForm{}, Signer: signer, Store: newMemStore(), IssuerName: "limes-test"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), requestValue{}, "request")
	req := IssueRequest{ClientID: testClientID,
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "a"},
		Audience: []string{audienceData}, Caveats: Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet, OpShare}},
	}
	c, _, err := issuer.issue(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := issuer.biscuit(ctx, asVerified(c)); err != nil {
		t.Fatal(err)
	}
	if len(key.got) == 0 {
		t.Fatal("the key's SignContext was never called")
	}
	for i, got := range key.got {
		if got.Value(requestValue{}) != "request" {
			t.Errorf("signature %d was made under another context", i)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := issuer.issue(cancelled, req); !errors.Is(err, context.Canceled) {
		t.Errorf("Issue under a cancelled context: err = %v, want context.Canceled", err)
	}
}

// verifySignature checks token's signature alone, as the verifier's first
// gate does.
func verifySignature(token string, pub ed25519.PublicKey) error {
	parts, err := jose.Split(token)
	if err != nil {
		return err
	}
	return verifyParts(parts, pub)
}

// decode reads token's claims without checking it.
func decode(token string) (Capability, error) {
	c, err := decodeToken(token)
	if err != nil {
		return Capability{}, err
	}
	return c.(Capability), nil
}

// opaqueKey hides the concrete key type, as a KMS or HSM signer does.
type opaqueKey struct{ crypto.Signer }

// A key the signer cannot see into — a KMS's — signs tokens the verifier
// accepts; a key that is not Ed25519 is refused up front.
func TestSignerSignsThroughAnOpaqueKey(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewSigner(SignerConfig{KeyID: testKID, Key: SealKey(opaqueKey{priv})})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	token, err := signer.sign(context.Background(), Capability{
		ID: uuid.New(), Issuer: "i", Subject: Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "agent"},
		Audience: []string{audienceData}, Caveats: Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet}}, IssuedAt: now, ExpiresAt: now.Add(time.Minute),
		ClientID: testClientID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := verifySignature(token, pub); err != nil {
		t.Fatalf("a token signed through an opaque key: %v", err)
	}
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for name, cfg := range map[string]SignerConfig{
		"no kid":      {Key: SealKey(priv)},
		"no key":      {KeyID: testKID},
		"a P-256 key": {KeyID: testKID, Key: SealKey(ec)},
	} {
		if _, err := NewSigner(cfg); err == nil {
			t.Errorf("NewSigner with %s: accepted", name)
		}
	}
}

// misreportingKey reports one public key and signs with another, as a KMS
// signer wired to the wrong key version does.
type misreportingKey struct {
	reported ed25519.PublicKey
	signs    ed25519.PrivateKey
}

func (k misreportingKey) Public() crypto.PublicKey { return k.reported }

func (k misreportingKey) Sign(r io.Reader, msg []byte, opts crypto.SignerOpts) ([]byte, error) {
	return k.signs.Sign(r, msg, opts)
}

// garbageKey reports a real public key and returns a signature's length of
// zero bytes, as a broken signer might.
type garbageKey struct{ reported ed25519.PublicKey }

func (k garbageKey) Public() crypto.PublicKey { return k.reported }

func (garbageKey) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	return make([]byte, ed25519.SignatureSize), nil
}

// onBehalfOfHolder is a user of c's tenant for c to act for.
func onBehalfOfHolder(c *Capability) *Principal {
	return &Principal{Type: PrincipalTypeUser, TenantID: c.Subject.TenantID, Subject: "user"}
}

// shortKey reports a real public key and returns a signature one byte short.
type shortKey struct{ reported ed25519.PublicKey }

func (k shortKey) Public() crypto.PublicKey { return k.reported }

func (shortKey) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	return make([]byte, ed25519.SignatureSize-1), nil
}

// errKeyUnavailable is the failure of a key held elsewhere.
var errKeyUnavailable = errors.New("key unavailable")

// failingKey reports a real public key and cannot sign, as a KMS that is
// down.
type failingKey struct{ reported ed25519.PublicKey }

func (k failingKey) Public() crypto.PublicKey { return k.reported }

func (failingKey) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	return nil, errKeyUnavailable
}

// A signature that does not verify under the key's own public key is never
// put in a token, whatever signed it: every verifier would refuse that token.
func TestSignerRefusesASignatureItsKeyDoesNotVerify(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	c := Capability{ClientID: testClientID,
		ID: uuid.New(), Issuer: "i", Subject: Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "agent"},
		Audience: []string{audienceData}, Caveats: Caveats{Requests: RequestCeiling{Unlimited: true}, Ops: []Op{OpGet}}, IssuedAt: now, ExpiresAt: now.Add(time.Minute),
	}
	for name, key := range map[string]crypto.Signer{
		"another key's signature": misreportingKey{reported: pub, signs: other},
		"no signature at all":     garbageKey{reported: pub},
		"a signature too short":   shortKey{reported: pub},
		"a key that fails":        failingKey{reported: pub},
	} {
		signer, err := NewSigner(SignerConfig{KeyID: testKID, Key: SealKey(key)})
		if err != nil {
			t.Fatal(err)
		}
		if token, err := signer.sign(context.Background(), c); err == nil {
			t.Errorf("%s: signed %q", name, token)
		}
	}
}
