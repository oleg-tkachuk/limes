package limes

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestSign_RoundTrip confirms Sign produces a token that Decode parses
// back into a Capability with the same load-bearing fields.
func TestSign_RoundTrip(t *testing.T) {
	t.Parallel()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := NewEd25519Signer("test-key-1", priv)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}

	tenantID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	cap := Capability{
		ID:     uuid.New(),
		Issuer: "limes-test",
		Subject: Principal{
			Type:     PrincipalAgent,
			TenantID: tenantID,
			Subject:  "agent-42",
			Agent: &AgentPrincipal{
				AgentType:    "research-orchestrator",
				AgentVersion: "1.0.0",
				RunID:        uuid.New(),
				Model:        "example-model-1",
			},
		},
		Audience:   []string{AudiencePlaneData, AudiencePlaneMCP},
		IssuedAt:   now,
		ExpiresAt:  now.Add(15 * time.Minute),
		Generation: 1,
		Caveats: Caveats{
			Ops:              []Op{OpGet, OpList, OpSearch},
			ResourcePrefixes: []string{"object://acme/run-42/"},
			MaxBudgetAmount:  NanosPerUnit / 2,
			MaxRequests:      100,
		},
	}

	token, err := signer.Sign(cap)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := VerifySignature(token, pub); err != nil {
		t.Fatalf("verify signature: %v", err)
	}

	got, err := Decode(token)
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
	if got.Caveats.MaxBudgetAmount != NanosPerUnit/2 {
		t.Errorf("MaxBudgetAmount: got %v, want 0.50", got.Caveats.MaxBudgetAmount)
	}
	if got.Generation != 1 {
		t.Errorf("Generation: got %d, want 1", got.Generation)
	}
	if !got.IssuedAt.Equal(cap.IssuedAt) {
		t.Errorf("IssuedAt round-trip drift: %s vs %s", got.IssuedAt, cap.IssuedAt)
	}
}

// TestSign_RejectsInvalid covers the validation guards in Sign.
func TestSign_RejectsInvalid(t *testing.T) {
	t.Parallel()

	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := NewEd25519Signer("k", priv)

	now := time.Now()
	base := func() Capability {
		return Capability{
			ID:     uuid.New(),
			Issuer: "i",
			Subject: Principal{
				Type:     PrincipalAgent,
				TenantID: uuid.New(),
			},
			Caveats:   Caveats{Ops: []Op{OpGet}},
			IssuedAt:  now,
			ExpiresAt: now.Add(time.Minute),
		}
	}

	cases := map[string]func(c *Capability){
		"missing ID":     func(c *Capability) { c.ID = uuid.Nil },
		"missing issuer": func(c *Capability) { c.Issuer = "" },
		"missing tenant": func(c *Capability) { c.Subject.TenantID = uuid.Nil },
		"empty ops":      func(c *Capability) { c.Caveats.Ops = nil },
		"reversed times": func(c *Capability) { c.ExpiresAt = c.IssuedAt.Add(-time.Minute) },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			c := base()
			mut(&c)
			if _, err := signer.Sign(c); err == nil {
				t.Fatalf("expected error, got nil")
			}
		})
	}
}

// TestSign_TamperDetection mutates the encoded token mid-stream and
// confirms VerifySignature rejects.
func TestSign_TamperDetection(t *testing.T) {
	t.Parallel()

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := NewEd25519Signer("k", priv)

	now := time.Now()
	cap := Capability{
		ID:        uuid.New(),
		Issuer:    "i",
		Subject:   Principal{TenantID: uuid.New(), Type: PrincipalAgent},
		Audience:  []string{AudiencePlaneData},
		Caveats:   Caveats{Ops: []Op{OpGet}},
		IssuedAt:  now,
		ExpiresAt: now.Add(time.Minute),
	}
	token, _ := signer.Sign(cap)

	// Flip a byte in the middle of the payload segment.
	mid := len(token) / 2
	tampered := token[:mid] + flipByte(token[mid]) + token[mid+1:]
	if err := VerifySignature(tampered, pub); !errors.Is(err, ErrInvalidSignature) {
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

	parent := Capability{
		ID:       uuid.New(),
		Issuer:   "i",
		Subject:  Principal{TenantID: tenantID, Type: PrincipalAgent},
		Audience: []string{AudiencePlaneData},
		Caveats: Caveats{
			Ops:              []Op{OpGet, OpList},
			ResourcePrefixes: []string{"object://acme/"},
			MaxBudgetAmount:  MustParseAmount("1.00"),
			MaxRequests:      50,
		},
		IssuedAt:  now,
		ExpiresAt: now.Add(15 * time.Minute),
	}

	cases := map[string]func(c *Capability){
		"extra op": func(c *Capability) {
			c.Caveats.Ops = append(c.Caveats.Ops, OpDelete)
		},
		"extra audience": func(c *Capability) {
			c.Audience = append(c.Audience, AudiencePlaneAdmin)
		},
		"longer ttl": func(c *Capability) {
			c.ExpiresAt = now.Add(time.Hour)
		},
		"wider budget": func(c *Capability) {
			c.Caveats.MaxBudgetAmount = 10 * NanosPerUnit
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
			c.Caveats.MaxRequests = 0
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
	parent := Capability{
		ID:       uuid.New(),
		Issuer:   "i",
		Subject:  Principal{TenantID: tenantID, Type: PrincipalAgent},
		Audience: []string{AudiencePlaneData},
		Caveats: Caveats{
			Ops:             []Op{OpGet},
			MaxBudgetAmount: MustParseAmount("10.0"),
			UnitCode:        "USD",
		},
		IssuedAt:  now,
		ExpiresAt: now.Add(15 * time.Minute),
	}
	child := parent
	child.ID = uuid.New()
	child.ParentID = parent.ID
	child.Caveats = Caveats{
		Ops:             []Op{OpGet},
		MaxBudgetAmount: MustParseAmount("5.0"),
		UnitCode:        "EUR", // ← mismatch
	}
	err := Narrows(parent, child)
	if !errors.Is(err, ErrUnitCodeMismatch) {
		t.Fatalf("expected ErrUnitCodeMismatch, got %v", err)
	}
}

// TestNarrows_AcceptsValidNarrowing covers the happy path: narrowing on
// every axis produces a valid child.
func TestNarrows_AcceptsValidNarrowing(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	now := time.Now().UTC()
	parent := Capability{
		ID:       uuid.New(),
		Issuer:   "i",
		Subject:  Principal{TenantID: tenantID, Type: PrincipalAgent},
		Audience: []string{AudiencePlaneData, AudiencePlaneMCP},
		Caveats: Caveats{
			Ops:              []Op{OpGet, OpList, OpSearch},
			ResourcePrefixes: []string{"object://acme/"},
			MaxBudgetAmount:  MustParseAmount("1.00"),
			MaxRequests:      100,
		},
		IssuedAt:  now,
		ExpiresAt: now.Add(time.Hour),
	}
	child := Capability{
		ID:       uuid.New(),
		Issuer:   "i",
		Subject:  Principal{TenantID: tenantID, Type: PrincipalAgent},
		Audience: []string{AudiencePlaneData},
		Caveats: Caveats{
			Ops:              []Op{OpGet},
			ResourcePrefixes: []string{"object://acme/run-42/"},
			MaxBudgetAmount:  NanosPerUnit / 10,
			MaxRequests:      10,
		},
		IssuedAt:  now,
		ExpiresAt: now.Add(15 * time.Minute),
		ParentID:  parent.ID,
	}
	if err := Narrows(parent, child); err != nil {
		t.Fatalf("expected narrowing OK, got %v", err)
	}
}
